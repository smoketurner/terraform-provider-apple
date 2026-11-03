package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func (e *testEnv) backgroundAsset(id string) (*gen.BackgroundAsset, error) {
	out, err := e.client.BackgroundAssetsGetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return &out.JSON200.Data, nil
}

func (e *testEnv) checkArchived(id *string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		a, err := e.backgroundAsset(*id)
		if err != nil {
			return err
		}
		if got := asc.Value(a.Attributes.Archived); got != want {
			return fmt.Errorf("asset pack %s archived = %t, want %t", *id, got, want)
		}
		return nil
	}
}

func (e *testEnv) createBackgroundAsset(appID, identifier string) func(ctx context.Context, c *asc.Client) error {
	return func(ctx context.Context, c *asc.Client) error {
		var body gen.BackgroundAssetCreateRequest
		body.Data.Type = gen.BackgroundAssetCreateRequestDataTypeBackgroundAssets
		body.Data.Attributes.AssetPackIdentifier = identifier
		body.Data.Relationships.App.Data.Id = appID
		body.Data.Relationships.App.Data.Type = gen.BackgroundAssetCreateRequestDataRelationshipsAppDataTypeApps
		out, err := c.BackgroundAssetsCreateInstanceWithResponse(ctx, body)
		return asc.Err(out, err)
	}
}

func backgroundAssetConfig(identifier string) string {
	return `
resource "apple_background_asset" "tutorial" {
  app_id                = local.app_id
  asset_pack_identifier = "` + identifier + `"
}
`
}

func TestAccBackgroundAsset(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	const address = "apple_background_asset.tutorial"
	var first, second string
	env.run(resource.TestCase{
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(env.checkArchived(&first, true), env.checkArchived(&second, true)),
		Steps: []resource.TestStep{
			{
				Config: locals + backgroundAssetConfig("Tutorial"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "app_id", appID),
					resource.TestCheckResourceAttr(address, "asset_pack_identifier", "Tutorial"),
					resource.TestCheckResourceAttrSet(address, "created_date"),
					captureAttr(address, "id", &first),
					env.checkArchived(&first, false),
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				// A new identifier replaces the asset pack; the old one is
				// archived.
				Config:           locals + backgroundAssetConfig("Levels"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &second),
					env.checkArchived(&first, true),
					env.checkArchived(&second, false),
				),
			},
			{
				// The first identifier again restores the archived asset pack.
				Config: locals + backgroundAssetConfig("Tutorial"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &first),
					env.checkArchived(&first, false),
					env.checkArchived(&second, true),
				),
			},
			{
				// Archived outside Terraform, it is restored.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.BackgroundAssetUpdateRequest
					body.Data.Type, body.Data.Id = gen.BackgroundAssetUpdateRequestDataTypeBackgroundAssets, first
					asc.Alloc(&body.Data.Attributes).Archived = new(true)
					out, err := c.BackgroundAssetsUpdateInstanceWithResponse(ctx, first, body)
					return asc.Err(out, err)
				}),
				Config:           locals + backgroundAssetConfig("Tutorial"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &first),
					env.checkArchived(&first, false),
				),
			},
			{
				// An asset pack that exists outside Terraform must be imported.
				PreConfig: env.outside(env.createBackgroundAsset(appID, "Extras")),
				Config: locals + backgroundAssetConfig("Tutorial") + `
resource "apple_background_asset" "extras" {
  app_id                = local.app_id
  asset_pack_identifier = "Extras"
}
`,
				ExpectError: regexp.MustCompile(`already has the asset pack "Extras"`),
			},
			{
				Config: locals + backgroundAssetConfig("Tutorial") + `
data "apple_background_assets" "all" {
  app_id     = local.app_id
  depends_on = [apple_background_asset.tutorial]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_background_assets.all", "asset_packs.#", "3"),
					resource.TestCheckTypeSetElemNestedAttrs("data.apple_background_assets.all", "asset_packs.*",
						map[string]string{"asset_pack_identifier": "Tutorial", "archived": "false"}),
					resource.TestCheckTypeSetElemNestedAttrs("data.apple_background_assets.all", "asset_packs.*",
						map[string]string{"asset_pack_identifier": "Levels", "archived": "true"}),
				),
			},
			{
				// Archived outside Terraform, then destroyed: nothing to do.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.BackgroundAssetUpdateRequest
					body.Data.Type, body.Data.Id = gen.BackgroundAssetUpdateRequestDataTypeBackgroundAssets, first
					asc.Alloc(&body.Data.Attributes).Archived = new(true)
					out, err := c.BackgroundAssetsUpdateInstanceWithResponse(ctx, first, body)
					return asc.Err(out, err)
				}),
				Config: locals,
			},
		},
	})
}

func TestAccBackgroundAssetErrors(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + `
resource "apple_background_asset" "bad" {
  app_id                = "missing"
  asset_pack_identifier = "Tutorial"
}
`,
				ExpectError: regexp.MustCompile(`Unable to create asset pack`),
			},
			{
				Config: locals + `
data "apple_background_assets" "missing" {
  app_id = "missing"
}
`,
				ExpectError: regexp.MustCompile(`Unable to list asset packs`),
			},
			{
				Config: locals + `
data "apple_background_asset_versions" "missing" {
  background_asset_id = "missing"
}
`,
				ExpectError: regexp.MustCompile(`Unable to list asset pack\s+versions`),
			},
		},
	})
}

func backgroundAssetVersionConfig(file, manifest string) string {
	cfg := backgroundAssetConfig("Tutorial") + `
resource "apple_background_asset_version" "tutorial" {
  background_asset_id = apple_background_asset.tutorial.id
  file                = "` + file + `"
`
	if manifest != "" {
		cfg += `  manifest_file       = "` + manifest + `"
`
	}
	return cfg + "}\n"
}

func writeManifest(t *testing.T, dir, contents string) (string, string) {
	t.Helper()
	p := filepath.Join(dir, "Manifest.json")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, asc.Checksum([]byte(contents))
}

func (e *testEnv) checkBackgroundAssetFiles(id *string, archive, manifest *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := e.fake.BackgroundAssetFileChecksum(*id, "ASSET"); got != *archive {
			return fmt.Errorf("version %s archive = %q, want %q", *id, got, *archive)
		}
		if got := e.fake.BackgroundAssetFileChecksum(*id, "MANIFEST"); got != *manifest {
			return fmt.Errorf("version %s manifest = %q, want %q", *id, got, *manifest)
		}
		return nil
	}
}

func (e *testEnv) checkInternalBetaState(id *string, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ctx := context.Background()
		out, err := e.client.BackgroundAssetVersionsGetInstanceWithResponse(ctx, *id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		rel, err := e.client.BackgroundAssetVersionInternalBetaReleasesGetInstanceWithResponse(ctx, out.JSON200.Data.Relationships.InternalBetaRelease.Data.Id, nil)
		if err := asc.Err(rel, err); err != nil {
			return err
		}
		if got := string(asc.Value(rel.JSON200.Data.Attributes.State)); got != want {
			return fmt.Errorf("version %s internal beta state = %s, want %s", *id, got, want)
		}
		return nil
	}
}

func TestAccBackgroundAssetVersion(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	archive, sum := writeMedia(t, dir, "Tutorial.aar", "tutorial ")
	manifest, manifestSum := writeManifest(t, dir, `{"assetPackID": "Tutorial"}`)
	const address = "apple_background_asset_version.tutorial"
	var first, second, third string
	none := ""
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + backgroundAssetVersionConfig(filepath.Join(dir, "missing.aar"), ""),
				ExpectError: regexp.MustCompile(`Cannot read asset pack file`),
			},
			{
				Config: locals + backgroundAssetVersionConfig(archive, manifest),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "checksum", sum),
					resource.TestCheckResourceAttr(address, "manifest_checksum", manifestSum),
					resource.TestCheckResourceAttr(address, "version", "1"),
					resource.TestCheckResourceAttr(address, "state", "COMPLETE"),
					resource.TestCheckResourceAttr(address, "internal_beta_state", "READY_FOR_TESTING"),
					resource.TestCheckNoResourceAttr(address, "external_beta_state"),
					resource.TestCheckNoResourceAttr(address, "app_store_state"),
					resource.TestCheckResourceAttrPair(address, "background_asset_id", "apple_background_asset.tutorial", "id"),
					captureAttr(address, "id", &first),
					env.checkBackgroundAssetFiles(&first, &sum, &manifestSum),
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"file", "manifest_file"}},
			{
				// Reviews release the version outside Terraform.
				PreConfig: func() {
					env.fake.ReleaseBackgroundAssetVersion(first, true, "READY_FOR_TESTING")
					env.fake.ReleaseBackgroundAssetVersion(first, false, "READY_FOR_DISTRIBUTION")
				},
				Config: locals + backgroundAssetVersionConfig(archive, manifest),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "external_beta_state", "READY_FOR_TESTING"),
					resource.TestCheckResourceAttr(address, "app_store_state", "READY_FOR_DISTRIBUTION"),
				),
			},
			{
				// A new archive is a new version, which supersedes the first
				// for internal testing.
				PreConfig:        func() { archive, sum = writeMedia(t, dir, "Tutorial.aar", "tutorial v2 ") },
				Config:           locals + backgroundAssetVersionConfig(archive, manifest),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "version", "2"),
					resource.TestCheckResourceAttrPtr(address, "checksum", &sum),
					resource.TestCheckNoResourceAttr(address, "external_beta_state"),
					captureAttr(address, "id", &second),
					env.checkBackgroundAssetFiles(&second, &sum, &manifestSum),
					env.checkInternalBetaState(&first, "SUPERSEDED"),
					env.checkInternalBetaState(&second, "READY_FOR_TESTING"),
				),
			},
			{
				// Dropping the manifest is a new version too.
				Config:           locals + backgroundAssetVersionConfig(archive, ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(address, "version", "3"),
					resource.TestCheckNoResourceAttr(address, "manifest_checksum"),
					captureAttr(address, "id", &third),
					env.checkBackgroundAssetFiles(&third, &sum, &none),
				),
			},
			{
				// Moving the archive without changing it changes nothing in
				// App Store Connect.
				PreConfig:        func() { archive, _ = writeMedia(t, dir, "Moved.aar", "tutorial v2 ") },
				Config:           locals + backgroundAssetVersionConfig(filepath.Join(dir, "Moved.aar"), ""),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &third),
					resource.TestCheckResourceAttr(address, "version", "3"),
				),
			},
			{
				Config: locals + backgroundAssetVersionConfig(archive, "") + `
data "apple_background_asset_versions" "all" {
  background_asset_id = apple_background_asset.tutorial.id
  depends_on          = [apple_background_asset_version.tutorial]
}

data "apple_background_asset_versions" "awaiting" {
  background_asset_id = apple_background_asset.tutorial.id
  states              = ["AWAITING_UPLOAD"]
  depends_on          = [apple_background_asset_version.tutorial]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apple_background_asset_versions.all", "versions.#", "3"),
					resource.TestCheckResourceAttrPtr("data.apple_background_asset_versions.all", "versions.0.id", &first),
					resource.TestCheckResourceAttr("data.apple_background_asset_versions.all", "versions.0.version", "1"),
					resource.TestCheckResourceAttr("data.apple_background_asset_versions.all", "versions.2.state", "COMPLETE"),
					resource.TestCheckResourceAttrSet("data.apple_background_asset_versions.all", "versions.2.created_date"),
					resource.TestCheckResourceAttr("data.apple_background_asset_versions.awaiting", "versions.#", "0"),
				),
			},
		},
	})
}

func TestAccBackgroundAssetVersionErrors(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	archive, _ := writeMedia(t, dir, "Tutorial.aar", "tutorial ")
	bad, _ := writeManifest(t, dir, "not json")
	const address = "apple_background_asset_version.tutorial"
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + backgroundAssetVersionConfig(archive, bad),
				ExpectError: regexp.MustCompile(`(?s)Unable to upload asset pack manifest.*MANIFEST_INVALID`),
			},
			{
				PreConfig:   func() { env.fake.FailNext("PUT", "/upload", 403) },
				Config:      locals + backgroundAssetVersionConfig(archive, ""),
				ExpectError: regexp.MustCompile(`Unable to upload asset pack archive`),
			},
			{
				PreConfig:   func() { env.fake.FailNext("POST", "/v1/backgroundAssetVersions", 409) },
				Config:      locals + backgroundAssetVersionConfig(archive, ""),
				ExpectError: regexp.MustCompile(`Unable to create asset pack version`),
			},
			{
				Config: locals + backgroundAssetVersionConfig(archive, ""),
				Check:  captureAttr(address, "id", &id),
			},
			{
				// A refresh that fails reports the error.
				PreConfig:   func() { env.fake.FailNext("GET", "/v1/backgroundAssetVersions/"+id, 403) },
				Config:      locals + backgroundAssetVersionConfig(archive, ""),
				ExpectError: regexp.MustCompile(`Unable to read asset pack version`),
			},
		},
	})
}
