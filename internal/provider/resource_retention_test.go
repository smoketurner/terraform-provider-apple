package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/smoketurner/terraform-provider-apple/internal/asc"
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// retentionPlacementConfig adds an image and a retention message placement
// showing it, with extra arguments on the placement.
func retentionPlacementConfig(dir, placementType, extra string) string {
	return `
resource "apple_app_asset_library_image" "stay" {
  app_id   = local.app_id
  file     = "` + filepath.Join(dir, "stay.png") + `"
  category = "CREATIVE_ASSETS"
}

resource "apple_app_asset_library_placement" "stay" {
  image_id        = apple_app_asset_library_image.stay.id
  placement_type  = "` + placementType + `"
  placement_group = "DEFAULT_PROFILE"
` + extra + `
}
`
}

func retentionMessageConfig(name, version, localization string) string {
	return `
resource "apple_retention_message" "stay" {
  app_id         = local.app_id
  reference_name = "` + name + `"
}

resource "apple_retention_message_version" "first" {
  retention_message_id = apple_retention_message.stay.id
` + version + `
}

data "apple_retention_messages" "all" {
  app_id     = local.app_id
  depends_on = [apple_retention_message.stay]
}
` + localization
}

func retentionLocalizationConfig(text string) string {
	return `
resource "apple_retention_message_localization" "en_US" {
  retention_message_version_id = apple_retention_message_version.first.id
  locale                       = "en-US"
` + text + `
}
`
}

func (e *testEnv) checkRetentionVersionState(id *string, want gen.RetentionMessageVersionAttributesState) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.RetentionMessageVersionsGetInstanceWithResponse(context.Background(), *id, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		if got := asc.Value(asc.Value(out.JSON200.Data.Attributes).State); got != want {
			return fmt.Errorf("retention message version is %s, want %s", got, want)
		}
		return nil
	}
}

func TestAccRetentionMessage(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	dir := t.TempDir()
	writeImage(t, dir, "stay.png", "stay ")
	placement := retentionPlacementConfig(dir, "RETENTION_MESSAGE_ASSET", "")
	withPlacement := `placement_id = apple_app_asset_library_placement.stay.id`
	text := retentionLocalizationConfig(`header = "Your cortado is waiting"
body = "Stay subscribed and keep your daily cortado coming."`)
	var messageID, versionID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// A retention message placement takes no localization.
				Config: locals + versionConfig("IOS", "1.0.0", "") + `
resource "apple_app_store_version_localization" "en_US" {
  version_id = apple_app_store_version.next.id
  locale     = "en-US"
}
` + retentionPlacementConfig(dir, "RETENTION_MESSAGE_ASSET", `app_store_version_localization_id = apple_app_store_version_localization.en_US.id`),
				ExpectError: regexp.MustCompile(`placement takes no localization`),
			},
			{
				// Every other placement needs one.
				Config:      locals + retentionPlacementConfig(dir, "APP_SCREENSHOT", ""),
				ExpectError: regexp.MustCompile(`Exactly one of`),
			},
			{
				Config: locals + placement + retentionMessageConfig("Stay with us", withPlacement, text),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_retention_message.stay", "id", &messageID),
					captureAttr("apple_retention_message_version.first", "id", &versionID),
					resource.TestCheckResourceAttr("apple_retention_message_version.first", "state", "PREPARE_FOR_SUBMISSION"),
					resource.TestCheckResourceAttrPair("apple_retention_message_version.first", "placement_id", "apple_app_asset_library_placement.stay", "id"),
					resource.TestCheckResourceAttr("data.apple_retention_messages.all", "retention_messages.#", "1"),
					resource.TestCheckResourceAttr("data.apple_retention_messages.all", "retention_messages.0.reference_name", "Stay with us"),
					resource.TestCheckResourceAttrPtr("data.apple_retention_messages.all", "retention_messages.0.id", &messageID),
				),
			},
			{
				ResourceName: "apple_retention_message.stay", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + messageID, nil },
			},
			{ResourceName: "apple_retention_message_version.first", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_app_asset_library_placement.stay", ImportState: true, ImportStateVerify: true},
			{ResourceName: "apple_retention_message_localization.en_US", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "apple_retention_message_localization.en_US", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return versionID + "/en-US", nil },
			},
			{
				ResourceName: "apple_retention_message_localization.en_US", ImportState: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return versionID + "/fr-FR", nil },
				ExpectError:       regexp.MustCompile(`No resource has the ID or version ID and locale`),
			},
			{
				// Text changes in place, and removed text clears.
				Config:           locals + placement + retentionMessageConfig("Stay with us", withPlacement, retentionLocalizationConfig(`header = "Before you go"`)),
				ConfigPlanChecks: expectAction("apple_retention_message_localization.en_US", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_retention_message_localization.en_US", "header", "Before you go"),
					resource.TestCheckNoResourceAttr("apple_retention_message_localization.en_US", "body"),
				),
			},
			{
				// A version has one localization per locale.
				Config: locals + placement + retentionMessageConfig("Stay with us", withPlacement, retentionLocalizationConfig(`header = "Before you go"`)) + `
resource "apple_retention_message_localization" "again" {
  retention_message_version_id = apple_retention_message_version.first.id
  locale                       = "en-US"
}
`,
				ExpectError: regexp.MustCompile(`already has a localization for\s+en-US`),
			},
			{
				// A localization deleted outside Terraform is created again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					all, err := retentionLocalizations(ctx, c, versionID)
					if err != nil {
						return err
					}
					out, err := c.RetentionMessageLocalizationsDeleteInstanceWithResponse(ctx, all[0].Id)
					return asc.Err(out, err)
				}),
				Config:           locals + placement + retentionMessageConfig("Stay with us", withPlacement, retentionLocalizationConfig(`header = "Before you go"`)),
				ConfigPlanChecks: expectAction("apple_retention_message_localization.en_US", plancheck.ResourceActionCreate),
			},
			{
				// A version can't change: dropping the image creates a new one,
				// and the old one stays in App Store Connect.
				Config:           locals + placement + retentionMessageConfig("Stay with us", "", ""),
				ConfigPlanChecks: expectAction("apple_retention_message_version.first", plancheck.ResourceActionDestroyBeforeCreate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apple_retention_message_version.first", "placement_id"),
					env.checkRetentionVersionState(&versionID, gen.RetentionMessageVersionAttributesStatePREPAREFORSUBMISSION),
				),
			},
			{
				// A new reference name creates a new message.
				Config:           locals + placement + retentionMessageConfig("Please stay", "", ""),
				ConfigPlanChecks: expectAction("apple_retention_message.stay", plancheck.ResourceActionDestroyBeforeCreate),
				Check:            resource.TestCheckResourceAttr("data.apple_retention_messages.all", "retention_messages.#", "1"),
			},
			{
				// A message deleted outside Terraform is created again, with
				// its version.
				PreConfig: func() {
					env.outside(func(ctx context.Context, c *asc.Client) error {
						list, err := c.AppsRetentionMessagesGetToManyRelatedWithResponse(ctx, appID, nil)
						if err := asc.Err(list, err); err != nil {
							return err
						}
						out, err := c.RetentionMessagesDeleteInstanceWithResponse(ctx, list.JSON200.Data[0].Id)
						return asc.Err(out, err)
					})()
				},
				Config: locals + placement + retentionMessageConfig("Please stay", "", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("apple_retention_message.stay", plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction("apple_retention_message_version.first", plancheck.ResourceActionCreate),
				}},
			},
		},
	})
}

func TestAccRetentionMessageLimit(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{{
			Config: locals + `
resource "apple_retention_message" "many" {
  count          = 11
  app_id         = local.app_id
  reference_name = "Message ${count.index}"
}
`,
			ExpectError: regexp.MustCompile(`already has 10 retention messages`),
		}},
	})
}

func TestAccSubmitRetentionMessageVersionAction(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	actions := `
action "apple_submit_retention_message_version" "first" {
  config {
    app_id                       = local.app_id
    retention_message_version_id = apple_retention_message_version.first.id
  }
}
action "apple_submit_retention_message_version" "prepared" {
  config {
    app_id                       = local.app_id
    retention_message_version_id = apple_retention_message_version.first.id
    platform                     = "MAC_OS"
    submit                       = false
  }
}
`
	text := retentionLocalizationConfig(`header = "Your cortado is waiting"`)
	var versionID string
	env.actionCase(
		resource.TestStep{
			// A version without localizations can't be submitted.
			Config:      locals + actions + retentionMessageConfig("Stay with us", "", "") + triggerAction("1", "apple_submit_retention_message_version.first"),
			ExpectError: regexp.MustCompile(`version has no localizations`),
		},
		// The localization is created first: the trigger doesn't depend on it.
		resource.TestStep{Config: locals + actions + retentionMessageConfig("Stay with us", "", text)},
		resource.TestStep{
			// Added to a submission without submitting it, the version is
			// ready for review.
			Config: locals + actions + retentionMessageConfig("Stay with us", "", text) + triggerAction("2", "apple_submit_retention_message_version.prepared"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_retention_message_version.first", "id", &versionID),
				env.checkRetentionVersionState(&versionID, gen.RetentionMessageVersionAttributesStateREADYFORREVIEW),
			),
		},
		resource.TestStep{
			// A version already in a submission can't be submitted again.
			Config:      locals + actions + retentionMessageConfig("Stay with us", "", text) + triggerAction("3", "apple_submit_retention_message_version.first"),
			ExpectError: regexp.MustCompile(`is READY_FOR_REVIEW, and can't be\s+submitted again`),
		},
		resource.TestStep{
			// An approved version takes no new localizations.
			PreConfig: func() { env.fake.ApproveRetentionMessageVersion(versionID) },
			Config: locals + actions + retentionMessageConfig("Stay with us", "", text) + `
resource "apple_retention_message_localization" "fr_FR" {
  retention_message_version_id = apple_retention_message_version.first.id
  locale                       = "fr-FR"
  header                       = "Avant de partir"
}
`,
			ExpectError: regexp.MustCompile(`approved retention message version takes no\s+new\s+localizations`),
		},
		resource.TestStep{
			// The state is read back.
			Config: locals + actions + retentionMessageConfig("Stay with us", "", text),
			Check:  resource.TestCheckResourceAttr("apple_retention_message_version.first", "state", "APPROVED"),
		},
	)
}

func TestAccSubmitRetentionMessageVersionForReview(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	config := locals + `
action "apple_submit_retention_message_version" "first" {
  config {
    app_id                       = local.app_id
    retention_message_version_id = apple_retention_message_version.first.id
  }
}
` + retentionMessageConfig("Stay with us", "", retentionLocalizationConfig(`header = "Your cortado is waiting"`))
	var versionID string
	env.actionCase(
		// The localization is created first: the trigger doesn't depend on it.
		resource.TestStep{Config: config},
		resource.TestStep{
			Config: config + triggerAction("1", "apple_submit_retention_message_version.first"),
			Check: resource.ComposeAggregateTestCheckFunc(
				captureAttr("apple_retention_message_version.first", "id", &versionID),
				env.checkRetentionVersionState(&versionID, gen.RetentionMessageVersionAttributesStateWAITINGFORREVIEW),
			),
		},
	)
}

func retentionOfferConfig(price, eligibility, links string) string {
	return subscriptionPricesConfig("") + `
resource "apple_retention_offer" "stay" {
  subscription_id = apple_subscription.monthly.id
  reference_name  = "Stay one month"
  offer_id        = "stay_one_month"
  offer_mode      = "PAY_UP_FRONT"
  duration        = "ONE_MONTH"
  period_count    = 1
  price_point_ids = [local.sub_points["USA-` + price + `"]]
` + eligibility + `
}
` + links
}

const retentionLinks = `
resource "apple_retention_message" "stay" {
  app_id         = local.app_id
  reference_name = "Stay with us"
}

resource "apple_retention_message" "other" {
  app_id         = local.app_id
  reference_name = "Before you go"
}

resource "apple_subscription_retention_message_offer" "stay" {
  subscription_id    = apple_subscription.monthly.id
  retention_offer_id = apple_retention_offer.stay.id
}
`

func assignedRetentionMessage(name string) string {
	return `
resource "apple_subscription_retention_message" "monthly" {
  subscription_id      = apple_subscription.monthly.id
  retention_message_id = apple_retention_message.` + name + `.id
}
`
}

// checkRetentionMessageOffers checks the offers a subscription's retention
// message presents.
func (e *testEnv) checkRetentionMessageOffers(subID *string, want ...*string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		out, err := e.client.SubscriptionsRetentionMessageOffersGetToManyRelationshipWithResponse(context.Background(), *subID, nil)
		if err := asc.Err(out, err); err != nil {
			return err
		}
		if len(out.JSON200.Data) != len(want) {
			return fmt.Errorf("subscription presents %d retention offers, want %d", len(out.JSON200.Data), len(want))
		}
		for i, w := range want {
			if out.JSON200.Data[i].Id != *w {
				return fmt.Errorf("retention offer %d is %s, want %s", i, out.JSON200.Data[i].Id, *w)
			}
		}
		return nil
	}
}

func TestAccRetentionOffer(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	const eligible = `paid_subscription_months = 3
wait_between_offers_months = 6`
	var subID, offerID, messageID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + retentionOfferConfig("2.99", `paid_subscription_months = 25`, ""),
				ExpectError: regexp.MustCompile(`paid_subscription_months`),
			},
			{
				Config:      locals + retentionOfferConfig("2.99", "paid_subscription_months = 3\nwait_between_offers_months = 1", ""),
				ExpectError: regexp.MustCompile(`wait_between_offers_months`),
			},
			{
				Config: locals + retentionOfferConfig("2.99", eligible, retentionLinks+assignedRetentionMessage("stay")),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription.monthly", "id", &subID),
					captureAttr("apple_retention_offer.stay", "id", &offerID),
					captureAttr("apple_retention_message.stay", "id", &messageID),
					resource.TestCheckResourceAttr("apple_retention_offer.stay", "plan_type", "UPFRONT"),
					resource.TestCheckResourceAttr("apple_retention_offer.stay", "price_point_ids.#", "1"),
					resource.TestCheckResourceAttrPtr("apple_subscription_retention_message.monthly", "retention_message_id", &messageID),
					env.checkRetentionMessageOffers(&subID, &offerID),
				),
			},
			{ResourceName: "apple_retention_offer.stay", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "apple_subscription_retention_message.monthly", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return subID, nil },
				ImportStateVerifyIdentifierAttribute: "subscription_id",
			},
			{
				ResourceName: "apple_subscription_retention_message_offer.stay", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return subID + "/" + offerID, nil },
				ImportStateVerifyIdentifierAttribute: "retention_offer_id",
			},
			{
				// Eligibility changes in place, and a removed wait clears.
				Config:           locals + retentionOfferConfig("2.99", `paid_subscription_months = 36`, retentionLinks+assignedRetentionMessage("stay")),
				ConfigPlanChecks: expectAction("apple_retention_offer.stay", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_retention_offer.stay", "paid_subscription_months", "36"),
					resource.TestCheckNoResourceAttr("apple_retention_offer.stay", "wait_between_offers_months"),
					resource.TestCheckResourceAttrPtr("apple_retention_offer.stay", "id", &offerID),
				),
			},
			{
				// Assigning another message replaces the subscription's.
				Config:           locals + retentionOfferConfig("2.99", `paid_subscription_months = 36`, retentionLinks+assignedRetentionMessage("other")),
				ConfigPlanChecks: expectAction("apple_subscription_retention_message.monthly", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttrPair("apple_subscription_retention_message.monthly", "retention_message_id", "apple_retention_message.other", "id"),
			},
			{
				// An offer removed from the message outside Terraform is added
				// again.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					out, err := c.SubscriptionsRetentionMessageOffersDeleteToManyRelationshipWithResponse(ctx, subID, retentionOfferLinkage(offerID))
					return asc.Err(out, err)
				}),
				Config:           locals + retentionOfferConfig("2.99", `paid_subscription_months = 36`, retentionLinks+assignedRetentionMessage("other")),
				ConfigPlanChecks: expectAction("apple_subscription_retention_message_offer.stay", plancheck.ResourceActionCreate),
				Check:            env.checkRetentionMessageOffers(&subID, &offerID),
			},
			{
				// A message assigned outside Terraform is replaced by the
				// configured one.
				PreConfig: env.outside(func(ctx context.Context, c *asc.Client) error {
					var body gen.SubscriptionRetentionMessageLinkageRequest
					body.Data.Id, body.Data.Type = messageID, gen.SubscriptionRetentionMessageLinkageRequestDataTypeRetentionMessages
					out, err := c.SubscriptionsRetentionMessageUpdateToOneRelationshipWithResponse(ctx, subID, body)
					return asc.Err(out, err)
				}),
				Config:           locals + retentionOfferConfig("2.99", `paid_subscription_months = 36`, retentionLinks+assignedRetentionMessage("other")),
				ConfigPlanChecks: expectAction("apple_subscription_retention_message.monthly", plancheck.ResourceActionUpdate),
			},
			{
				// A new price replaces the offer, and its link with it.
				Config: locals + retentionOfferConfig("1.99", `paid_subscription_months = 36`, retentionLinks+assignedRetentionMessage("other")),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("apple_retention_offer.stay", plancheck.ResourceActionDestroyBeforeCreate),
					plancheck.ExpectResourceAction("apple_subscription_retention_message_offer.stay", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				// Destroying the assignment leaves the message assigned, and
				// removing the link stops presenting the offer.
				Config: locals + retentionOfferConfig("1.99", `paid_subscription_months = 36`, `
resource "apple_retention_message" "other" {
  app_id         = local.app_id
  reference_name = "Before you go"
}
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					env.checkRetentionMessageOffers(&subID),
					func(*terraform.State) error {
						out, err := env.client.SubscriptionsRetentionMessageGetToOneRelationshipWithResponse(context.Background(), subID)
						if err := asc.Err(out, err); err != nil {
							return err
						}
						if out.JSON200.Data.Id == "" {
							return errors.New("the subscription's retention message was unassigned")
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccRetentionOfferOfAnotherSubscription(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				// A paid offer needs prices.
				Config: locals + subscriptionConfig("") + `
resource "apple_retention_offer" "stay" {
  subscription_id          = apple_subscription.monthly.id
  reference_name           = "Stay one month"
  offer_id                 = "stay_one_month"
  offer_mode               = "PAY_UP_FRONT"
  duration                 = "ONE_MONTH"
  period_count             = 1
  paid_subscription_months = 3
}
`,
				ExpectError: regexp.MustCompile(`paid offer needs prices`),
			},
			{
				// A subscription presents only its own offers, and takes only
				// its app's messages.
				Config: locals + subscriptionConfig("") + `
resource "apple_subscription" "yearly" {
  subscription_group_id = apple_subscription_group.pro.id
  product_id            = "com.example.pro.yearly"
  name                  = "Pro Yearly"
  period                = "ONE_YEAR"
}

resource "apple_retention_offer" "trial" {
  subscription_id          = apple_subscription.monthly.id
  reference_name           = "Stay a week"
  offer_id                 = "stay_a_week"
  offer_mode               = "FREE_TRIAL"
  duration                 = "ONE_WEEK"
  period_count             = 1
  paid_subscription_months = 1
}

resource "apple_subscription_retention_message_offer" "wrong" {
  subscription_id    = apple_subscription.yearly.id
  retention_offer_id = apple_retention_offer.trial.id
}
`,
				ExpectError: regexp.MustCompile(`subscription has no retentionOffers with id`),
			},
			{
				Config: locals + subscriptionConfig("") + `
resource "apple_subscription_retention_message" "missing" {
  subscription_id      = apple_subscription.monthly.id
  retention_message_id = "missing"
}
`,
				ExpectError: regexp.MustCompile(`app has no retentionMessages with id`),
			},
		},
	})
}
