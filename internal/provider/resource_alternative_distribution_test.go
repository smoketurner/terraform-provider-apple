package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
)

func distributionDomainConfig(name string) string {
	return `
resource "apple_alternative_distribution_domain" "web" {
  domain         = "example.com"
  reference_name = "` + name + `"
}

data "apple_alternative_distribution_domains" "all" {
  depends_on = [apple_alternative_distribution_domain.web]
}
`
}

func TestAccAlternativeDistributionDomain(t *testing.T) {
	env := newTestEnv(t)
	const address = "apple_alternative_distribution_domain.web"
	var id, replaced string
	gone := func(id *string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			out, err := env.client.AlternativeDistributionDomainsGetInstanceWithResponse(context.Background(), *id, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("domain %s still exists (err %w)", *id, err)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		CheckDestroy: gone(&replaced),
		Steps: []resource.TestStep{
			{
				Config: distributionDomainConfig("Example"),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					resource.TestCheckResourceAttrSet(address, "created_date"),
					resource.TestCheckResourceAttr("data.apple_alternative_distribution_domains.all", "domains.#", "1"),
					resource.TestCheckResourceAttr("data.apple_alternative_distribution_domains.all", "domains.0.domain", "example.com"),
					resource.TestCheckResourceAttr("data.apple_alternative_distribution_domains.all", "domains.0.reference_name", "Example"),
				),
			},
			{ResourceName: address, ImportState: true, ImportStateVerify: true},
			{
				// A domain is added once.
				Config: distributionDomainConfig("Example") + `
resource "apple_alternative_distribution_domain" "again" {
  domain         = "example.com"
  reference_name = "Again"
}
`,
				ExpectError: regexp.MustCompile(`already\s+added`),
			},
			{
				// A domain can't change, so a new name replaces it.
				Config:           distributionDomainConfig("Renamed"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionDestroyBeforeCreate),
				Check:            resource.ComposeAggregateTestCheckFunc(captureAttr(address, "id", &replaced), gone(&id)),
			},
			{
				// A domain deleted outside Terraform is added again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AlternativeDistributionDomainsDeleteInstanceWithResponse(ctx, replaced)
					return asc.Err(out, err)
				}),
				Config:           distributionDomainConfig("Renamed"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check:            captureAttr(address, "id", &replaced),
			},
		},
	})
}

const distributionKeys = `
resource "apple_alternative_distribution_key" "account" {
  public_key = "-----BEGIN PUBLIC KEY-----\nMFkwEwYH\n-----END PUBLIC KEY-----\n"
}

resource "apple_alternative_distribution_key" "app" {
  app_id     = local.app_id
  public_key = "-----BEGIN PUBLIC KEY-----\nMFkwAppKey\n-----END PUBLIC KEY-----\n"
}

data "apple_alternative_distribution_key" "app" {
  app_id     = local.app_id
  depends_on = [apple_alternative_distribution_key.app]
}

data "apple_alternative_distribution_keys" "all" {
  depends_on = [apple_alternative_distribution_key.app, apple_alternative_distribution_key.account]
}

data "apple_alternative_distribution_keys" "apps" {
  app_specific = true
  depends_on   = [apple_alternative_distribution_key.app, apple_alternative_distribution_key.account]
}
`

func TestAccAlternativeDistributionKey(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	const account, app = "apple_alternative_distribution_key.account", "apple_alternative_distribution_key.app"
	var accountID, appKeyID string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.AlternativeDistributionKeysGetCollectionWithResponse(context.Background(), nil)
			if err := asc.Err(out, err); err != nil {
				return err
			}
			if n := len(out.JSON200.Data); n != 0 {
				return fmt.Errorf("%d keys are left", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      `data "apple_alternative_distribution_key" "none" { app_id = "` + appID + `" }`,
				ExpectError: regexp.MustCompile(`No alternative distribution key matches`),
			},
			{
				Config: locals + distributionKeys,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(account, "id", &accountID),
					captureAttr(app, "id", &appKeyID),
					resource.TestCheckNoResourceAttr(account, "app_id"),
					resource.TestCheckResourceAttrPair("data.apple_alternative_distribution_key.app", "id", app, "id"),
					resource.TestCheckResourceAttrPair("data.apple_alternative_distribution_key.app", "public_key", app, "public_key"),
					resource.TestCheckResourceAttr("data.apple_alternative_distribution_keys.all", "keys.#", "2"),
					resource.TestCheckResourceAttr("data.apple_alternative_distribution_keys.apps", "keys.#", "1"),
					resource.TestCheckResourceAttrPair("data.apple_alternative_distribution_keys.apps", "keys.0.id", app, "id"),
				),
			},
			{ResourceName: account, ImportState: true, ImportStateVerify: true},
			{
				// App Store Connect doesn't report a key's app, so the
				// import ID names it.
				ResourceName: app, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + appKeyID, nil },
			},
			{
				// An app has one key of its own.
				Config: locals + distributionKeys + `
resource "apple_alternative_distribution_key" "second" {
  app_id     = local.app_id
  public_key = "other"
  depends_on = [apple_alternative_distribution_key.app]
}
`,
				ExpectError: regexp.MustCompile(`key\s+already\s+exists`),
			},
			{
				// A key can't change, so a new public key replaces it.
				Config: locals + `
resource "apple_alternative_distribution_key" "account" {
  public_key = "-----BEGIN PUBLIC KEY-----\nMFkwNew\n-----END PUBLIC KEY-----\n"
}
`,
				ConfigPlanChecks: expectAction(account, plancheck.ResourceActionDestroyBeforeCreate),
				Check: func(*terraform.State) error {
					out, err := env.client.AlternativeDistributionKeysGetCollectionWithResponse(context.Background(), nil)
					if err := asc.Err(out, err); err != nil {
						return err
					}
					if keys := out.JSON200.Data; len(keys) != 1 || keys[0].Id == accountID || *keys[0].Attributes.PublicKey != "-----BEGIN PUBLIC KEY-----\nMFkwNew\n-----END PUBLIC KEY-----\n" {
						return fmt.Errorf("unexpected keys %+v", keys)
					}
					return nil
				},
			},
		},
	})
}

func searchDetailConfig(url string) string {
	return `
resource "apple_marketplace_search_detail" "catalog" {
  app_id      = local.app_id
  catalog_url = "` + url + `"
}
`
}

func TestAccMarketplaceSearchDetail(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	const address = "apple_marketplace_search_detail.catalog"
	catalog := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			out, err := env.client.AppsMarketplaceSearchDetailGetToOneRelatedWithResponse(context.Background(), appID, nil)
			err = asc.Err(out, err)
			switch {
			case want == "" && asc.IsNotFound(err):
				return nil
			case err != nil:
				return err
			}
			if got := *out.JSON200.Data.Attributes.CatalogUrl; got != want {
				return fmt.Errorf("catalog URL = %q, want %q", got, want)
			}
			return nil
		}
	}
	var id string
	env.run(resource.TestCase{
		CheckDestroy: catalog(""),
		Steps: []resource.TestStep{
			{
				Config: locals + searchDetailConfig("https://example.com/catalog"),
				Check:  resource.ComposeAggregateTestCheckFunc(captureAttr(address, "id", &id), catalog("https://example.com/catalog")),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID, nil },
			},
			{
				Config:           locals + searchDetailConfig("https://example.com/v2/catalog"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(address, "id", &id), catalog("https://example.com/v2/catalog"),
				),
			},
			{
				// An app has one search detail.
				Config: locals + searchDetailConfig("https://example.com/v2/catalog") + `
resource "apple_marketplace_search_detail" "again" {
  app_id      = local.app_id
  catalog_url = "https://example.com/other"
  depends_on  = [apple_marketplace_search_detail.catalog]
}
`,
				ExpectError: regexp.MustCompile(`already\s+has\s+a\s+marketplace\s+search\s+detail`),
			},
			{
				// A detail deleted outside Terraform is added again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.MarketplaceSearchDetailsDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           locals + searchDetailConfig("https://example.com/v2/catalog"),
				ConfigPlanChecks: expectAction(address, plancheck.ResourceActionCreate),
				Check:            catalog("https://example.com/v2/catalog"),
			},
		},
	})
}

const distributionPackage = `
resource "apple_alternative_distribution_package" "next" {
  app_store_version_id = apple_app_store_version.next.id
}
`

const packageVersions = `
data "apple_alternative_distribution_package_versions" "all" {
  package_id = apple_alternative_distribution_package.next.id
}

data "apple_alternative_distribution_package_versions" "current" {
  package_id = apple_alternative_distribution_package.next.id
  states     = ["COMPLETED"]
}

data "apple_alternative_distribution_package_version" "current" {
  id = data.apple_alternative_distribution_package_versions.current.versions[0].id
}
`

func TestAccAlternativeDistributionPackage(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	const address = "apple_alternative_distribution_package.next"
	const all, current = "data.apple_alternative_distribution_package_versions.all", "data.apple_alternative_distribution_package_versions.current"
	const detail = "data.apple_alternative_distribution_package_version.current"
	base := locals + versionConfig("IOS", "1.0.0", "")
	var id, versionID, second string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: base + distributionPackage + `
data "apple_alternative_distribution_package_versions" "all" {
  package_id = apple_alternative_distribution_package.next.id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr(address, "id", &id),
					captureAttr("apple_app_store_version.next", "id", &versionID),
					resource.TestCheckResourceAttr(all, "versions.#", "0"),
				),
			},
			{
				ResourceName: address, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return versionID, nil },
			},
			{
				// Apple builds the package's versions; the newest replaces
				// the others.
				PreConfig: func() {
					env.fake.AddPackageVersion(id, "1")
					second = env.fake.AddPackageVersion(id, "2")
				},
				Config: base + distributionPackage + packageVersions,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(all, "versions.#", "2"),
					resource.TestCheckResourceAttr(all, "versions.0.state", "REPLACED"),
					resource.TestCheckResourceAttr(current, "versions.#", "1"),
					resource.TestCheckResourceAttrPtr(current, "versions.0.id", &second),
					resource.TestCheckResourceAttr(current, "versions.0.version", "2"),
					resource.TestCheckResourceAttrSet(current, "versions.0.url"),
					resource.TestCheckResourceAttrSet(current, "versions.0.url_expiration_date"),
					resource.TestCheckResourceAttr(detail, "state", "COMPLETED"),
					resource.TestCheckResourceAttrPair(detail, "file_checksum", current, "versions.0.file_checksum"),
					resource.TestCheckResourceAttr(detail, "variants.#", "1"),
					resource.TestMatchResourceAttr(detail, "variants.0.alternative_distribution_key_blob", regexp.MustCompile(`^blob-`)),
					resource.TestCheckResourceAttr(detail, "deltas.#", "1"),
					resource.TestMatchResourceAttr(detail, "deltas.0.file_checksum", regexp.MustCompile(`^sha256-`)),
				),
			},
			{
				// A version has one package, which a second resource takes
				// over instead of creating another.
				Config: base + distributionPackage + `
resource "apple_alternative_distribution_package" "same" {
  app_store_version_id = apple_app_store_version.next.id
  depends_on           = [apple_alternative_distribution_package.next]
}
`,
				Check: resource.TestCheckResourceAttrPtr("apple_alternative_distribution_package.same", "id", &id),
			},
			{
				Config:      `data "apple_alternative_distribution_package_version" "missing" { id = "nope" }`,
				ExpectError: regexp.MustCompile(`No alternative distribution package version matches`),
			},
			{
				// The API can't delete a package, so destroying the resource
				// leaves it.
				Config: base,
				Check: func(*terraform.State) error {
					out, err := env.client.AppStoreVersionsAlternativeDistributionPackageGetToOneRelatedWithResponse(context.Background(), versionID, nil)
					if err := asc.Err(out, err); err != nil {
						return err
					}
					if got := out.JSON200.Data.Id; got != id {
						return fmt.Errorf("package = %s, want %s", got, id)
					}
					return nil
				},
			},
		},
	})
}
