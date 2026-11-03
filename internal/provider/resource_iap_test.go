package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/asctest"
)

func iapConfig(extra string) string {
	return `
resource "apple_in_app_purchase" "coins" {
  app_id     = local.app_id
  product_id = "com.example.coins100"
  type       = "CONSUMABLE"
  name       = "100 Coins"
` + extra + `
}
`
}

func (e *testEnv) checkIAPVersions(id *string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := e.fake.IAPVersionStates(*id); !slices.Equal(got, want) {
			return fmt.Errorf("in-app purchase versions = %v, want %v", got, want)
		}
		return nil
	}
}

func TestAccInAppPurchase(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	var id string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.InAppPurchasesV2GetInstanceWithResponse(context.Background(), id, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("in-app purchase %s still exists: %w", id, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      locals + regexp.MustCompile(`CONSUMABLE`).ReplaceAllString(iapConfig(""), "AUTO_RENEWABLE"),
				ExpectError: regexp.MustCompile(`type value must be one of`),
			},
			{
				Config: locals + iapConfig(`review_note = "Tap Store, then Coins."`) + `
data "apple_in_app_purchases" "consumables" {
  app_id     = local.app_id
  types      = ["CONSUMABLE"]
  depends_on = [apple_in_app_purchase.coins]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_in_app_purchase.coins", "state", "MISSING_METADATA"),
					resource.TestCheckResourceAttr("apple_in_app_purchase.coins", "family_sharable", "false"),
					resource.TestCheckResourceAttr("data.apple_in_app_purchases.consumables", "in_app_purchases.0.product_id", "com.example.coins100"),
					captureAttr("apple_in_app_purchase.coins", "id", &id),
				),
			},
			{
				ResourceName: "apple_in_app_purchase.coins", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + id, nil },
			},
			{
				// A second purchase can't reuse the product ID.
				Config: locals + iapConfig("") + `
resource "apple_in_app_purchase" "copy" {
  app_id     = local.app_id
  product_id = "com.example.coins100"
  type       = "CONSUMABLE"
  name       = "Copy"
}
`,
				ExpectError: regexp.MustCompile(`is\s+already\s+used`),
			},
			{
				// Removing the review note clears it.
				Config: locals + iapConfig(`family_sharable = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_in_app_purchase.coins", "review_note"),
					resource.TestCheckResourceAttr("apple_in_app_purchase.coins", "family_sharable", "true"),
				),
			},
			{
				Config:      locals + iapConfig(`family_sharable = false`),
				ExpectError: regexp.MustCompile(`Family Sharing can't be turned off`),
			},
			{Config: locals + iapConfig(`family_sharable = true`)},
		},
	})
}

// TestAccInAppPurchaseApprovedStays checks that destroying an approved
// purchase leaves it.
func TestAccInAppPurchaseApprovedStays(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var id string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.InAppPurchasesV2GetInstanceWithResponse(context.Background(), id, nil)
			return asc.Err(out, err)
		},
		Steps: []resource.TestStep{
			{
				Config: locals + iapConfig("") + iapLocalizationConfig("en-US", "100 Coins", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase.coins", "id", &id),
					func(*terraform.State) error { env.fake.ApproveInAppPurchase(id); return nil },
				),
			},
		},
	})
}

func iapLocalizationConfig(locale, name, extra string) string {
	return `
resource "apple_in_app_purchase_localization" "` + regexp.MustCompile(`-`).ReplaceAllString(locale, "_") + `" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  locale             = "` + locale + `"
  name               = "` + name + `"
` + extra + `
}
`
}

func TestAccInAppPurchaseLocalization(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	both := func(name string) string {
		return locals + iapConfig("") + iapLocalizationConfig("en-US", name, `description = "A pile of coins."`) +
			iapLocalizationConfig("fr-FR", "100 pièces", "")
	}
	var id, firstEnglish string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + iapConfig("") + iapLocalizationConfig("en-US", "X", ""),
				ExpectError: regexp.MustCompile(`string length must be between 2 and 35`),
			},
			{
				// The first localization starts the first version.
				Config: both("100 Coins"),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase.coins", "id", &id),
					captureAttr("apple_in_app_purchase_localization.en_US", "id", &firstEnglish),
					env.checkIAPVersions(&id, "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttr("apple_in_app_purchase_localization.fr_FR", "name", "100 pièces"),
				),
			},
			{
				ResourceName: "apple_in_app_purchase_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return id + "/en-US", nil },
				ImportStateVerifyIdentifierAttribute: "locale",
			},
			{
				// Edits while preparing change the same version.
				Config: locals + iapConfig("") + iapLocalizationConfig("en-US", "100 Coins", "") + iapLocalizationConfig("fr-FR", "100 pièces", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_in_app_purchase_localization.en_US", "description"),
					resource.TestCheckResourceAttrPtr("apple_in_app_purchase_localization.en_US", "id", &firstEnglish),
					env.checkIAPVersions(&id, "PREPARE_FOR_SUBMISSION"),
				),
			},
			{
				// After approval, a change starts the next version, which has
				// copies of the localizations.
				PreConfig:        func() { env.fake.ApproveInAppPurchase(id) },
				Config:           both("A Hundred Coins"),
				ConfigPlanChecks: expectAction("apple_in_app_purchase_localization.fr_FR", plancheck.ResourceActionNoop),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkIAPVersions(&id, "APPROVED", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttr("apple_in_app_purchase_localization.en_US", "name", "A Hundred Coins"),
					func(s *terraform.State) error {
						if s.RootModule().Resources["apple_in_app_purchase_localization.en_US"].Primary.ID == firstEnglish {
							return errors.New("the new version's localization was not used")
						}
						return nil
					},
				),
			},
			{
				// Removing a localization deletes it from the new version.
				Config: locals + iapConfig("") + iapLocalizationConfig("en-US", "A Hundred Coins", `description = "A pile of coins."`),
				Check: func(*terraform.State) error {
					draft, _, _, err := iapMetadata.find(context.Background(), env.client, id)
					if err != nil {
						return err
					}
					r := &versionedLocalizationResource{api: iapLocalizationAPI, client: env.client}
					if _, err := r.find(context.Background(), draft.id, "fr-FR"); err == nil {
						return errors.New("fr-FR was not deleted")
					}
					return nil
				},
			},
		},
	})
}

func TestAccInAppPurchaseFiles(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	image, imageSum := writeMedia(t, dir, "coins.png", "coins ")
	shot, shotSum := writeMedia(t, dir, "store.png", "store ")
	cfg := func() string {
		return locals + iapConfig("") + iapLocalizationConfig("en-US", "100 Coins", "") + `
resource "apple_in_app_purchase_image" "coins" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  file               = "` + image + `"
}

resource "apple_in_app_purchase_review_screenshot" "coins" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  file               = "` + shot + `"
}
`
	}
	var id, imageID, shotID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg(),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase.coins", "id", &id),
					captureAttr("apple_in_app_purchase_image.coins", "id", &imageID),
					captureAttr("apple_in_app_purchase_review_screenshot.coins", "id", &shotID),
					env.checkFile(&imageID, &imageSum),
					env.checkFile(&shotID, &shotSum),
				),
			},
			{
				// The image's checksum isn't reported, so a refresh must not
				// replace it.
				Config:           cfg(),
				ConfigPlanChecks: expectAction("apple_in_app_purchase_image.coins", plancheck.ResourceActionNoop),
			},
			{
				// After approval, a new image goes to the next version, and
				// replaces the copy of the old one.
				PreConfig: func() {
					env.fake.ApproveInAppPurchase(id)
					image, imageSum = writeMedia(t, dir, "coins.png", "shiny coins ")
				},
				Config:           cfg(),
				ConfigPlanChecks: expectAction("apple_in_app_purchase_image.coins", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase_image.coins", "id", &imageID),
					env.checkFile(&imageID, &imageSum),
					env.checkIAPVersions(&id, "APPROVED", "PREPARE_FOR_SUBMISSION"),
					func(*terraform.State) error {
						draft, _, _, err := iapMetadata.find(context.Background(), env.client, id)
						if err != nil {
							return err
						}
						out, err := env.client.InAppPurchaseVersionsImageGetToOneRelatedWithResponse(context.Background(), draft.id, nil)
						if err := asc.Err(out, err); err != nil {
							return err
						}
						if out.JSON200.Data.Id != imageID {
							return fmt.Errorf("draft image = %s, want %s", out.JSON200.Data.Id, imageID)
						}
						return nil
					},
				),
			},
			{
				// The review screenshot belongs to the purchase; a new one
				// replaces it.
				PreConfig:        func() { shot, shotSum = writeMedia(t, dir, "store.png", "new store ") },
				Config:           cfg(),
				ConfigPlanChecks: expectAction("apple_in_app_purchase_review_screenshot.coins", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase_review_screenshot.coins", "id", &shotID),
					env.checkFile(&shotID, &shotSum),
				),
			},
		},
	})
}

func TestAccSubmitInAppPurchaseAction(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "7", Marketing: "1.0.0"})
	actions := `
action "apple_submit_in_app_purchase" "alone" {
  config {
    app_id             = local.app_id
    in_app_purchase_id = apple_in_app_purchase.coins.id
  }
}
action "apple_submit_in_app_purchase" "with_version" {
  config {
    app_id             = local.app_id
    in_app_purchase_id = apple_in_app_purchase.coins.id
    submit             = false
  }
}
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
`
	base := locals + actions + iapConfig("") + versionConfig("IOS", "1.0.0", `build_id = "`+buildID+`"`)
	var id, versionID string
	env.actionCase(
		resource.TestStep{
			// A purchase without localizations can't be submitted.
			Config:      base + triggerAction("1", "apple_submit_in_app_purchase.alone"),
			ExpectError: regexp.MustCompile(`no version\s+being\s+prepared`),
		},
		resource.TestStep{Config: base + iapLocalizationConfig("en-US", "100 Coins", "")},
		resource.TestStep{
			// Submitted with the app's version, both wait for review.
			Config: base + iapLocalizationConfig("en-US", "100 Coins", "") +
				triggerAction("2", "apple_submit_in_app_purchase.with_version", "apple_submit_for_review.next"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_in_app_purchase.coins", "id", &id),
				captureAttr("apple_app_store_version.next", "id", &versionID),
				env.checkIAPVersions(&id, "WAITING_FOR_REVIEW"),
				env.checkVersionState(&versionID, "WAITING_FOR_REVIEW"),
			),
		},
		resource.TestStep{
			// Metadata can't change while in review.
			Config:      base + iapLocalizationConfig("en-US", "A Hundred Coins", ""),
			ExpectError: regexp.MustCompile(`in-app purchase is in\s+review`),
		},
		resource.TestStep{
			// Canceling returns the purchase version to the developer; the
			// next change starts a new version, which is submitted alone.
			Config: base + iapLocalizationConfig("en-US", "100 Coins", "") + triggerAction("3", "apple_cancel_review_submission.next"),
			Check:  env.checkIAPVersions(&id, "DEVELOPER_REJECTED"),
		},
		resource.TestStep{Config: base + iapLocalizationConfig("en-US", "A Hundred Coins", "")},
		resource.TestStep{
			Config: base + iapLocalizationConfig("en-US", "A Hundred Coins", "") + triggerAction("4", "apple_submit_in_app_purchase.alone"),
			Check:  env.checkIAPVersions(&id, "DEVELOPER_REJECTED", "WAITING_FOR_REVIEW"),
		},
	)
}
