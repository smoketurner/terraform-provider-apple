package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

func versionConfig(platform, version, extra string) string {
	return `
resource "apple_app_store_version" "next" {
  app_id         = local.app_id
  platform       = "` + platform + `"
  version_string = "` + version + `"
` + extra + `
}
`
}

// versionState returns a version's state, or "" when it doesn't exist.
func (e *testEnv) versionState(id string) (string, error) {
	out, err := e.client.AppStoreVersionsGetInstanceWithResponse(context.Background(), id, nil)
	if err := asc.Err(out, err); err != nil {
		if asc.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return string(*out.JSON200.Data.Attributes.AppVersionState), nil
}

func (e *testEnv) checkVersionState(id *string, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := e.versionState(*id)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("version %s state = %q, want %q", *id, got, want)
		}
		return nil
	}
}

func TestAccAppStoreVersion(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "7", Marketing: "1.0.0"})
	macBuild := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "8", Marketing: "1.0.0", Platform: gen.PlatformMACOS})
	var first, second string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			// The released version stays; the unsubmitted one is deleted.
			for id, want := range map[string]string{first: "READY_FOR_DISTRIBUTION", second: ""} {
				got, err := env.versionState(id)
				if err != nil {
					return err
				}
				if got != want {
					return fmt.Errorf("version %s state = %q after destroy, want %q", id, got, want)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      locals + versionConfig("WATCH_OS", "1.0.0", ""),
				ExpectError: regexp.MustCompile(`platform value must be one of`),
			},
			{
				Config:      locals + versionConfig("IOS", "1.0.0", `release_type = "SCHEDULED"`+"\n"+`earliest_release_date = "2026-12-01T08:00:00-08:00"`),
				ExpectError: regexp.MustCompile(`must be an RFC 3339 time in UTC`),
			},
			{
				// The app's first version already exists, so it is managed.
				Config: locals + versionConfig("IOS", "1.0.0", `copyright = "2026 Example Inc."`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_version.next", "state", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttr("apple_app_store_version.next", "release_type", "AFTER_APPROVAL"),
					resource.TestCheckResourceAttr("apple_app_store_version.next", "review_type", "APP_STORE"),
					resource.TestCheckNoResourceAttr("apple_app_store_version.next", "build_id"),
					captureAttr("apple_app_store_version.next", "id", &first),
					func(*terraform.State) error {
						if first != env.fake.FirstVersion(appID) {
							return fmt.Errorf("version %s was created, want the existing %s", first, env.fake.FirstVersion(appID))
						}
						return nil
					},
				),
			},
			{ResourceName: "apple_app_store_version.next", ImportState: true, ImportStateVerify: true},
			{
				Config:      locals + versionConfig("IOS", "1.0.0", fmt.Sprintf("build_id = %q", macBuild)),
				ExpectError: regexp.MustCompile(`The build is for another platform`),
			},
			{
				// Removing the copyright clears it.
				Config: locals + versionConfig("IOS", "1.0.0", fmt.Sprintf(`build_id = %q
release_type = "SCHEDULED"
earliest_release_date = "2026-12-01T16:00:00Z"`, buildID)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_app_store_version.next", "copyright"),
					resource.TestCheckResourceAttr("apple_app_store_version.next", "earliest_release_date", "2026-12-01T16:00:00Z"),
					resource.TestCheckResourceAttr("apple_app_store_version.next", "build_id", buildID),
					func(*terraform.State) error {
						if got := env.fake.VersionBuild(first); got != buildID {
							return fmt.Errorf("version build = %q, want %q", got, buildID)
						}
						return nil
					},
				),
			},
			{
				// A released version shows as such, with nothing to change.
				PreConfig: func() { env.fake.ReleaseApp(appID) },
				Config: locals + versionConfig("IOS", "1.0.0", fmt.Sprintf(`build_id = %q
release_type = "SCHEDULED"
earliest_release_date = "2026-12-01T16:00:00Z"`, buildID)),
				ConfigPlanChecks: expectAction("apple_app_store_version.next", plancheck.ResourceActionNoop),
				Check:            resource.TestCheckResourceAttr("apple_app_store_version.next", "state", "READY_FOR_DISTRIBUTION"),
			},
			{
				// Changing a released version creates the next one, and
				// leaves the released one alone.
				Config:           locals + versionConfig("IOS", "1.1.0", `copyright = "2026 Example Inc."`),
				ConfigPlanChecks: expectAction("apple_app_store_version.next", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_version.next", "state", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckNoResourceAttr("apple_app_store_version.next", "build_id"),
					captureAttr("apple_app_store_version.next", "id", &second),
					env.checkVersionState(&first, "READY_FOR_DISTRIBUTION"),
				),
			},
			{
				Config:      locals + versionConfig("IOS", "1.0.0", ""),
				ExpectError: regexp.MustCompile(`The version number has been previously used`),
			},
			{
				// A version can be renamed while it's being prepared.
				Config:           locals + versionConfig("IOS", "1.2.0", `copyright = "2026 Example Inc."`),
				ConfigPlanChecks: expectAction("apple_app_store_version.next", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_app_store_version.next", "version_string", "1.2.0"),
			},
		},
	})
}

// TestAccAppStoreVersionDrift checks that a version deleted outside
// Terraform is created again.
func TestAccAppStoreVersionDrift(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + versionConfig("MAC_OS", "2.0", ""),
				Check:  captureAttr("apple_app_store_version.next", "id", &id),
			},
			{
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.AppStoreVersionsDeleteInstanceWithResponse(ctx, id)
					return asc.Err(out, err)
				}),
				Config:           locals + versionConfig("MAC_OS", "2.0", ""),
				ConfigPlanChecks: expectAction("apple_app_store_version.next", plancheck.ResourceActionCreate),
			},
		},
	})
}

func versionEnglishConfig(extra string) string {
	return versionConfig("IOS", "1.0.0", "release_type = \"MANUAL\"") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id  = apple_app_store_version.next.id
  locale      = "en-US"
  description = "An example app."
  support_url = "https://example.com/support"
` + extra + `
}
`
}

func versionMetaConfig(extra string) string {
	return versionEnglishConfig(extra) + `
resource "apple_app_store_version_localization" "fr_FR" {
  version_id  = apple_app_store_version.next.id
  locale      = "fr-FR"
  description = "Une application d'exemple."
}
`
}

func TestAccAppStoreVersionLocalization(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	var versionID string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			// The released version keeps its localizations.
			out, err := env.client.AppStoreVersionsAppStoreVersionLocalizationsGetToManyRelatedWithResponse(context.Background(), versionID, nil)
			if err := asc.Err(out, err); err != nil {
				return err
			}
			if len(out.JSON200.Data) != 1 {
				return fmt.Errorf("released version has %d localizations, want 1", len(out.JSON200.Data))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      locals + versionMetaConfig(`keywords = "`+fmt.Sprintf("%0101d", 0)+`"`),
				ExpectError: regexp.MustCompile(`string length must be at most 100`),
			},
			{
				Config: locals + versionMetaConfig(`keywords = "example,demo"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_version_localization.en_US", "keywords", "example,demo"),
					resource.TestCheckResourceAttr("apple_app_store_version_localization.fr_FR", "description", "Une application d'exemple."),
					captureAttr("apple_app_store_version.next", "id", &versionID),
				),
			},
			{
				ResourceName: "apple_app_store_version_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return versionID + "/en-US", nil }, ImportStateVerifyIdentifierAttribute: "locale",
			},
			{
				Config:           locals + versionMetaConfig(""),
				ConfigPlanChecks: expectAction("apple_app_store_version_localization.en_US", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckNoResourceAttr("apple_app_store_version_localization.en_US", "keywords"),
			},
			{
				// Removing a localization deletes it.
				Config: locals + versionEnglishConfig(""),
				Check: func(*terraform.State) error {
					r := &versionLocalizationResource{client: env.client}
					if _, err := r.find(context.Background(), versionID, "fr-FR"); err == nil {
						return errors.New("fr-FR was not deleted")
					}
					return nil
				},
			},
			{
				// Once the version is released its metadata can't change.
				PreConfig:   func() { env.fake.ReleaseApp(appID) },
				Config:      locals + versionEnglishConfig(`keywords = "example"`),
				ExpectError: regexp.MustCompile(`cannot be edited in its current state`),
			},
			{
				// The localizations of a released version can't be deleted;
				// destroy leaves them.
				Config: locals + versionEnglishConfig(""),
			},
		},
	})
}

func reviewDetailConfig(attrs string) string {
	return versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_review_detail" "next" {
  version_id = apple_app_store_version.next.id
` + attrs + `
}
`
}

func TestAccAppStoreReviewDetail(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + reviewDetailConfig(`contact_email = "not an email"`),
				ExpectError: regexp.MustCompile(`must be an email address`),
			},
			{
				Config: locals + reviewDetailConfig(`contact_first_name = "Ann"
contact_email = "ann@example.com"
notes = "Tap the logo five times for the debug menu."`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_review_detail.next", "contact_first_name", "Ann"),
					resource.TestCheckResourceAttr("apple_app_store_review_detail.next", "demo_account_required", "false"),
				),
			},
			{
				ResourceName: "apple_app_store_review_detail.next", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: attrIDFunc("apple_app_store_review_detail.next", "version_id"), ImportStateVerifyIdentifierAttribute: "version_id",
			},
			{
				// Removing the notes clears them.
				Config: locals + reviewDetailConfig(`contact_first_name = "Ann"
contact_email = "ann@example.com"
demo_account_required = true
demo_account_name = "reviewer"
demo_account_password = "hunter2"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_app_store_review_detail.next", "notes"),
					resource.TestCheckResourceAttr("apple_app_store_review_detail.next", "demo_account_required", "true"),
					resource.TestCheckResourceAttr("apple_app_store_review_detail.next", "demo_account_password", "hunter2"),
				),
			},
		},
	})
}

func phasedReleaseConfig(paused string) string {
	return versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_phased_release" "next" {
  version_id = apple_app_store_version.next.id
` + paused + `
}
`
}

func TestAccAppStoreVersionPhasedRelease(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// Pausing before the release has no effect yet.
				Config: locals + phasedReleaseConfig("paused = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_version_phased_release.next", "state", "INACTIVE"),
					resource.TestCheckResourceAttr("apple_app_store_version_phased_release.next", "paused", "true"),
				),
			},
			{
				ResourceName: "apple_app_store_version_phased_release.next", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: attrIDFunc("apple_app_store_version_phased_release.next", "version_id"), ImportStateVerifyIdentifierAttribute: "version_id",
				// paused is not visible until the release starts.
				ImportStateVerifyIgnore: []string{"paused"},
			},
			{
				// Removing it before the release deletes it, and adding it
				// back creates a new one.
				Config: locals + versionConfig("IOS", "1.0.0", ""),
			},
			{Config: locals + phasedReleaseConfig("")},
			{
				// Once released, the phased release runs, and can be paused.
				PreConfig: func() { env.fake.ReleaseApp(appID) },
				Config:    locals + phasedReleaseConfig("paused = true"),
				Check:     resource.TestCheckResourceAttr("apple_app_store_version_phased_release.next", "state", "PAUSED"),
			},
			{
				Config: locals + phasedReleaseConfig(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_store_version_phased_release.next", "state", "ACTIVE"),
					resource.TestCheckResourceAttr("apple_app_store_version_phased_release.next", "paused", "false"),
				),
			},
		},
	})
}

const encryptionDeclarationConfig = `
resource "apple_app_encryption_declaration" "app" {
  app_id                            = local.app_id
  app_description                   = "Uses HTTPS only."
  contains_proprietary_cryptography = false
  contains_third_party_cryptography = false
  available_on_french_store         = true
}
`

func TestAccAppEncryptionDeclaration(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + encryptionDeclarationConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_encryption_declaration.app", "exempt", "true"),
					resource.TestCheckResourceAttr("apple_app_encryption_declaration.app", "state", "CREATED"),
				),
			},
			{ResourceName: "apple_app_encryption_declaration.app", ImportState: true, ImportStateVerify: true},
			{
				Config:           locals + regexp.MustCompile(`contains_third_party_cryptography = false`).ReplaceAllString(encryptionDeclarationConfig, "contains_third_party_cryptography = true"),
				ConfigPlanChecks: expectAction("apple_app_encryption_declaration.app", plancheck.ResourceActionDestroyBeforeCreate),
				Check:            resource.TestCheckResourceAttr("apple_app_encryption_declaration.app", "exempt", "false"),
			},
		},
	})
}

func TestAccReviewActions(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "7", Marketing: "1.0.0"})
	actions := `
action "apple_submit_for_review" "next" {
  config {
    version_id = apple_app_store_version.next.id
  }
}
action "apple_cancel_review_submission" "next" {
  config {
    version_id = apple_app_store_version.next.id
  }
}
action "apple_release_version" "next" {
  config {
    version_id = apple_app_store_version.next.id
  }
}
action "apple_assign_encryption_declaration" "build" {
  config {
    build_id       = "` + buildID + `"
    declaration_id = apple_app_encryption_declaration.app.id
  }
}
`
	withBuild := locals + actions + encryptionDeclarationConfig + versionConfig("IOS", "1.0.0", `release_type = "MANUAL"
build_id = "`+buildID+`"`)
	var versionID string
	env.actionCase(
		resource.TestStep{
			// A version needs a build to be submitted.
			Config:      locals + actions + encryptionDeclarationConfig + versionConfig("IOS", "1.0.0", `release_type = "MANUAL"`) + triggerAction("1", "apple_submit_for_review.next"),
			ExpectError: regexp.MustCompile(`The version has no build`),
		},
		resource.TestStep{
			Config: withBuild + triggerAction("2", "apple_assign_encryption_declaration.build", "apple_submit_for_review.next"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_app_store_version.next", "id", &versionID),
				env.checkVersionState(&versionID, "WAITING_FOR_REVIEW"),
				func(*terraform.State) error {
					out, err := env.client.BuildsGetInstanceWithResponse(context.Background(), buildID, nil)
					if err := asc.Err(out, err); err != nil {
						return err
					}
					if got := out.JSON200.Data.Attributes.UsesNonExemptEncryption; got == nil || *got {
						return fmt.Errorf("build usesNonExemptEncryption = %v, want false", got)
					}
					return nil
				},
			),
		},
		resource.TestStep{
			Config: withBuild + triggerAction("3", "apple_cancel_review_submission.next"),
			Check:  env.checkVersionState(&versionID, "DEVELOPER_REJECTED"),
		},
		resource.TestStep{
			Config:      withBuild + triggerAction("4", "apple_cancel_review_submission.next"),
			ExpectError: regexp.MustCompile(`has no IOS review\s+submission in review`),
		},
		resource.TestStep{
			Config:      withBuild + triggerAction("5", "apple_release_version.next"),
			ExpectError: regexp.MustCompile(`Only a version in Pending Developer Release can be released`),
		},
		resource.TestStep{
			// Submitted again and approved, it waits for its manual release.
			Config: withBuild + triggerAction("6", "apple_submit_for_review.next"),
			Check: func(*terraform.State) error {
				env.fake.ApproveVersion(versionID)
				return env.checkVersionState(&versionID, "PENDING_DEVELOPER_RELEASE")(nil)
			},
		},
		resource.TestStep{
			Config: withBuild + triggerAction("7", "apple_release_version.next"),
			Check:  env.checkVersionState(&versionID, "READY_FOR_DISTRIBUTION"),
		},
	)
}
