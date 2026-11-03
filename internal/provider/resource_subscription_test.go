package provider

import (
	"context"
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

func subscriptionConfig(extra string) string {
	return `
resource "apple_subscription_group" "pro" {
  app_id         = local.app_id
  reference_name = "Pro"
}

resource "apple_subscription" "monthly" {
  subscription_group_id = apple_subscription_group.pro.id
  product_id            = "com.example.pro.monthly"
  name                  = "Pro Monthly"
  period                = "ONE_MONTH"
` + extra + `
}
`
}

func (e *testEnv) checkMetaVersions(id *string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := e.fake.MetaVersionStates(*id); !slices.Equal(got, want) {
			return fmt.Errorf("versions of %s = %v, want %v", *id, got, want)
		}
		return nil
	}
}

// checkSubscriptionState checks a subscription's state through the API: it
// changes with its localizations, which the subscription resource's state
// doesn't see until the next refresh.
func (e *testEnv) checkSubscriptionState(id *string, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.SubscriptionsGetInstanceWithResponse(context.Background(), *id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		if got := string(*out.JSON200.Data.Attributes.State); got != want {
			return fmt.Errorf("subscription state = %s, want %s", got, want)
		}
		return nil
	}
}

func TestAccSubscription(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	var groupID, subID string
	env.run(resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			out, err := env.client.SubscriptionGroupsGetInstanceWithResponse(context.Background(), groupID, nil)
			if err := asc.Err(out, err); !asc.IsNotFound(err) {
				return fmt.Errorf("subscription group %s still exists: %w", groupID, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config:      locals + regexp.MustCompile(`ONE_MONTH`).ReplaceAllString(subscriptionConfig(""), "MONTHLY"),
				ExpectError: regexp.MustCompile(`period value must be one of`),
			},
			{
				Config: locals + subscriptionConfig(`review_note = "Tap Upgrade."`) + `
resource "apple_subscription" "yearly" {
  subscription_group_id = apple_subscription_group.pro.id
  product_id            = "com.example.pro.yearly"
  name                  = "Pro Yearly"
  period                = "ONE_YEAR"
  group_level           = 1
}

data "apple_subscription_groups" "app" {
  app_id     = local.app_id
  depends_on = [apple_subscription_group.pro]
}

data "apple_subscriptions" "pro" {
  subscription_group_id = apple_subscription_group.pro.id
  depends_on            = [apple_subscription.monthly, apple_subscription.yearly]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription_group.pro", "id", &groupID),
					captureAttr("apple_subscription.monthly", "id", &subID),
					resource.TestCheckResourceAttr("apple_subscription.monthly", "state", "MISSING_METADATA"),
					resource.TestCheckResourceAttrSet("apple_subscription.monthly", "group_level"),
					resource.TestCheckResourceAttr("apple_subscription.yearly", "group_level", "1"),
					resource.TestCheckResourceAttr("data.apple_subscription_groups.app", "subscription_groups.0.reference_name", "Pro"),
					resource.TestCheckResourceAttr("data.apple_subscriptions.pro", "subscriptions.#", "2"),
				),
			},
			{
				ResourceName: "apple_subscription_group.pro", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + groupID, nil },
			},
			{ResourceName: "apple_subscription.monthly", ImportState: true, ImportStateVerify: true},
			{
				// A subscription's product ID can't reuse an in-app purchase's.
				Config:      locals + subscriptionConfig("") + regexp.MustCompile(`com.example.coins100`).ReplaceAllString(iapConfig(""), "com.example.pro.monthly"),
				ExpectError: regexp.MustCompile(`is\s+already\s+used`),
			},
			{
				Config: locals + subscriptionConfig(`family_sharable = true`) + `
resource "apple_subscription_group" "pro_renamed" {
  app_id         = local.app_id
  reference_name = "Pro Plus"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_subscription.monthly", "review_note"),
					resource.TestCheckResourceAttr("apple_subscription.monthly", "family_sharable", "true"),
				),
			},
			{
				Config:      locals + subscriptionConfig(`family_sharable = false`),
				ExpectError: regexp.MustCompile(`Family Sharing can't be turned off`),
			},
			{Config: locals + subscriptionConfig(`family_sharable = true`)},
		},
	})
}

func subscriptionMetaConfig(subName, groupName string) string {
	return subscriptionConfig("") + `
resource "apple_subscription_group_localization" "en_US" {
  subscription_group_id = apple_subscription_group.pro.id
  locale                = "en-US"
  name                  = "` + groupName + `"
  custom_app_name       = "Example Pro"
}

resource "apple_subscription_localization" "en_US" {
  subscription_id = apple_subscription.monthly.id
  locale          = "en-US"
  name            = "` + subName + `"
  description     = "Everything in Example."
}
`
}

func TestAccSubscriptionMetadata(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	dir := t.TempDir()
	image, imageSum := writeMedia(t, dir, "pro.png", "pro ")
	shot, shotSum := writeMedia(t, dir, "upgrade.png", "upgrade ")
	files := `
resource "apple_subscription_image" "monthly" {
  subscription_id = apple_subscription.monthly.id
  file            = "` + image + `"
}

resource "apple_subscription_review_screenshot" "monthly" {
  subscription_id = apple_subscription.monthly.id
  file            = "` + shot + `"
}
`
	var groupID, subID, imageID, shotID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + regexp.MustCompile(`Example Pro"`).ReplaceAllString(subscriptionMetaConfig("Pro", "Pro"), `This custom app name is far too long"`),
				ExpectError: regexp.MustCompile(`string length must be between 0 and 30`),
			},
			{
				Config: locals + subscriptionMetaConfig("Pro Monthly", "Example Pro") + files,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription_group.pro", "id", &groupID),
					captureAttr("apple_subscription.monthly", "id", &subID),
					captureAttr("apple_subscription_image.monthly", "id", &imageID),
					captureAttr("apple_subscription_review_screenshot.monthly", "id", &shotID),
					env.checkMetaVersions(&groupID, "PREPARE_FOR_SUBMISSION"),
					env.checkMetaVersions(&subID, "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttr("apple_subscription_group_localization.en_US", "custom_app_name", "Example Pro"),
					env.checkSubscriptionState(&subID, "READY_TO_SUBMIT"),
					env.checkFile(&imageID, &imageSum),
					env.checkFile(&shotID, &shotSum),
				),
			},
			{
				ResourceName: "apple_subscription_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return subID + "/en-US", nil },
				ImportStateVerifyIdentifierAttribute: "locale",
			},
			{
				ResourceName: "apple_subscription_group_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return groupID + "/en-US", nil },
				ImportStateVerifyIdentifierAttribute: "locale",
			},
			{
				// After approval, changes start the next versions, which have
				// copies of the localizations and image.
				PreConfig:        func() { env.fake.ApproveSubscription(subID) },
				Config:           locals + subscriptionMetaConfig("Pro, Monthly", "Example Pro") + files,
				ConfigPlanChecks: expectAction("apple_subscription_group_localization.en_US", plancheck.ResourceActionNoop),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkMetaVersions(&subID, "APPROVED", "PREPARE_FOR_SUBMISSION"),
					env.checkMetaVersions(&groupID, "APPROVED"),
					resource.TestCheckResourceAttr("apple_subscription_localization.en_US", "name", "Pro, Monthly"),
				),
			},
			{
				Config: locals + subscriptionMetaConfig("Pro, Monthly", "Example Pro, Again") + files,
				Check:  env.checkMetaVersions(&groupID, "APPROVED", "PREPARE_FOR_SUBMISSION"),
			},
		},
	})
}

func TestAccSubmitSubscriptionAction(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	buildID := env.fake.AddBuild(appID, asctest.BuildOptions{Version: "7", Marketing: "1.0.0"})
	actions := `
action "apple_submit_subscription" "with_version" {
  config {
    app_id          = local.app_id
    subscription_id = apple_subscription.monthly.id
    submit          = false
  }
}
action "apple_submit_for_review" "next" {
  config {
    version_id = apple_app_store_version.next.id
  }
}
`
	base := locals + actions + versionConfig("IOS", "1.0.0", `build_id = "`+buildID+`"`)
	var groupID, subID string
	env.actionCase(
		resource.TestStep{
			Config:      base + subscriptionConfig("") + triggerAction("1", "apple_submit_subscription.with_version"),
			ExpectError: regexp.MustCompile(`no version\s+being\s+prepared`),
		},
		resource.TestStep{Config: base + subscriptionMetaConfig("Pro Monthly", "Example Pro")},
		resource.TestStep{
			// The subscription and its group go to review with the version.
			Config: base + subscriptionMetaConfig("Pro Monthly", "Example Pro") +
				triggerAction("2", "apple_submit_subscription.with_version", "apple_submit_for_review.next"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_subscription_group.pro", "id", &groupID),
				captureAttr("apple_subscription.monthly", "id", &subID),
				env.checkMetaVersions(&subID, "WAITING_FOR_REVIEW"),
				env.checkMetaVersions(&groupID, "WAITING_FOR_REVIEW"),
				env.checkSubscriptionState(&subID, "WAITING_FOR_REVIEW"),
			),
		},
		resource.TestStep{
			Config:      base + subscriptionMetaConfig("Pro, Monthly", "Example Pro"),
			ExpectError: regexp.MustCompile(`subscription is in\s+review`),
		},
		resource.TestStep{Config: base + subscriptionMetaConfig("Pro Monthly", "Example Pro")},
	)
}
