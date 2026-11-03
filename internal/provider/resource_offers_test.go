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
)

func promotedConfig(order string) string {
	return iapConfig("") + subscriptionConfig("") + `
resource "apple_promoted_purchase" "coins" {
  app_id                = local.app_id
  in_app_purchase_id    = apple_in_app_purchase.coins.id
  visible_for_all_users = true
}

resource "apple_promoted_purchase" "pro" {
  app_id                = local.app_id
  subscription_id       = apple_subscription.monthly.id
  visible_for_all_users = false
  enabled               = false
}
` + order
}

func promotionOrder(first, second string) string {
	return `
resource "apple_promoted_purchase_order" "app" {
  app_id                = local.app_id
  promoted_purchase_ids = [apple_promoted_purchase.` + first + `.id, apple_promoted_purchase.` + second + `.id]
}
`
}

func TestAccPromotedPurchase(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	var coinsID, proID string
	order := func(want ...*string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			out, err := env.client.AppsPromotedPurchasesGetToManyRelatedWithResponse(context.Background(), appID, nil)
			if err := asc.Err(out, err); err != nil {
				return err
			}
			var got, wantIDs []string
			for _, p := range out.JSON200.Data {
				got = append(got, p.Id)
			}
			for _, w := range want {
				wantIDs = append(wantIDs, *w)
			}
			if !slices.Equal(got, wantIDs) {
				return fmt.Errorf("promotion order = %v, want %v", got, wantIDs)
			}
			return nil
		}
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + iapConfig("") + `
resource "apple_promoted_purchase" "both" {
  app_id                = local.app_id
  in_app_purchase_id    = apple_in_app_purchase.coins.id
  subscription_id       = "S1"
  visible_for_all_users = true
}
`,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
			{
				Config: locals + promotedConfig(promotionOrder("coins", "pro")),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_promoted_purchase.coins", "id", &coinsID),
					captureAttr("apple_promoted_purchase.pro", "id", &proID),
					resource.TestCheckResourceAttr("apple_promoted_purchase.coins", "enabled", "true"),
					resource.TestCheckResourceAttr("apple_promoted_purchase.pro", "enabled", "false"),
					order(&coinsID, &proID),
				),
			},
			{
				ResourceName: "apple_promoted_purchase.pro", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return appID + "/" + proID, nil },
			},
			{ResourceName: "apple_promoted_purchase_order.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				Config:           locals + promotedConfig(promotionOrder("pro", "coins")),
				ConfigPlanChecks: expectAction("apple_promoted_purchase_order.app", plancheck.ResourceActionUpdate),
				Check:            order(&proID, &coinsID),
			},
			{
				// The order must name every promotion.
				Config: locals + promotedConfig(`
resource "apple_promoted_purchase_order" "app" {
  app_id                = local.app_id
  promoted_purchase_ids = [apple_promoted_purchase.pro.id]
}
`),
				ExpectError: regexp.MustCompile(`must list each of the app's promoted\s+purchases once`),
			},
			{Config: locals + promotedConfig(promotionOrder("pro", "coins"))},
		},
	})
}

func introOfferConfig(extra string) string {
	return subscriptionPricesConfig("") + `
resource "apple_subscription_introductory_offer" "trial" {
  subscription_id   = apple_subscription.monthly.id
  territory         = "USA"
  offer_mode        = "FREE_TRIAL"
  duration          = "ONE_WEEK"
  number_of_periods = 1
` + extra + `
}
`
}

func TestAccIntroductoryOffer(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var subID, offerID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + introOfferConfig(`price_point_id = local.sub_points["USA-0.99"]`),
				ExpectError: regexp.MustCompile(`A free trial has no price point`),
			},
			{
				Config: locals + introOfferConfig(`end_date = "2099-06-30"`) + `
resource "apple_subscription_introductory_offer" "canada" {
  subscription_id   = apple_subscription.monthly.id
  territory         = "CAN"
  price_point_id    = local.sub_points["CAN-0.99"]
  offer_mode        = "PAY_AS_YOU_GO"
  duration          = "ONE_MONTH"
  number_of_periods = 3
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription.monthly", "id", &subID),
					captureAttr("apple_subscription_introductory_offer.trial", "id", &offerID),
					resource.TestCheckResourceAttr("apple_subscription_introductory_offer.trial", "plan_type", "UPFRONT"),
					resource.TestCheckResourceAttrSet("apple_subscription_introductory_offer.canada", "price_point_id"),
				),
			},
			{
				ResourceName: "apple_subscription_introductory_offer.trial", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return subID + "/" + offerID, nil },
			},
			{
				// Removing the end date clears it in place.
				Config:           locals + introOfferConfig(""),
				ConfigPlanChecks: expectAction("apple_subscription_introductory_offer.trial", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckNoResourceAttr("apple_subscription_introductory_offer.trial", "end_date"),
			},
			{
				Config:           locals + regexp.MustCompile(`ONE_WEEK`).ReplaceAllString(introOfferConfig(""), "TWO_WEEKS"),
				ConfigPlanChecks: expectAction("apple_subscription_introductory_offer.trial", plancheck.ResourceActionDestroyBeforeCreate),
			},
		},
	})
}

func promoOfferConfig(prices string) string {
	return subscriptionPricesConfig("") + `
resource "apple_subscription_promotional_offer" "half" {
  subscription_id   = apple_subscription.monthly.id
  name              = "Half off"
  offer_code        = "half_off"
  offer_mode        = "PAY_AS_YOU_GO"
  duration          = "ONE_MONTH"
  number_of_periods = 2
  prices            = [` + prices + `]
}
`
}

func offerPrice(territory, price string) string {
	return `{ territory = "` + territory + `", price_point_id = local.sub_points["` + territory + "-" + price + `"] }`
}

func TestAccPromotionalOffer(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + promoOfferConfig(`{ territory = "GBR", price_point_id = local.sub_points["USA-0.99"] }`),
				ExpectError: regexp.MustCompile(`price point is for another territory`),
			},
			{
				Config: locals + promoOfferConfig(offerPrice("USA", "0.99")+", "+offerPrice("GBR", "0.99")),
				Check:  resource.TestCheckResourceAttr("apple_subscription_promotional_offer.half", "prices.#", "2"),
			},
			{ResourceName: "apple_subscription_promotional_offer.half", ImportState: true, ImportStateVerify: true},
			{
				// Prices change in place.
				Config:           locals + promoOfferConfig(offerPrice("USA", "1.99")),
				ConfigPlanChecks: expectAction("apple_subscription_promotional_offer.half", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_subscription_promotional_offer.half", "prices.#", "1"),
					resource.TestCheckTypeSetElemNestedAttrs("apple_subscription_promotional_offer.half", "prices.*", map[string]string{"territory": "USA"}),
				),
			},
			{
				Config:           locals + regexp.MustCompile(`Half off`).ReplaceAllString(promoOfferConfig(offerPrice("USA", "1.99")), "Half price"),
				ConfigPlanChecks: expectAction("apple_subscription_promotional_offer.half", plancheck.ResourceActionDestroyBeforeCreate),
			},
		},
	})
}

func winBackConfig(priority, extra string) string {
	return subscriptionPricesConfig("") + `
resource "apple_win_back_offer" "come_back" {
  subscription_id                  = apple_subscription.monthly.id
  reference_name                   = "Come back"
  offer_id                         = "come_back"
  offer_mode                       = "PAY_UP_FRONT"
  duration                         = "THREE_MONTHS"
  period_count                     = 1
  prices                           = [` + offerPrice("USA", "2.99") + `]
  paid_subscription_months         = 3
  months_since_last_subscribed_min = 2
  start_date                       = "2099-01-01"
  priority                         = "` + priority + `"
` + extra + `
}
`
}

func TestAccWinBackOffer(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var subID, offerID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + winBackConfig("NORMAL", `months_since_last_subscribed_max = 1`),
				ExpectError: regexp.MustCompile(`minimum months since last subscribed is above the\s+maximum`),
			},
			{
				Config: locals + winBackConfig("NORMAL", `months_since_last_subscribed_max = 12
wait_between_offers_months = 6
end_date = "2099-12-31"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription.monthly", "id", &subID),
					captureAttr("apple_win_back_offer.come_back", "id", &offerID),
					resource.TestCheckResourceAttr("apple_win_back_offer.come_back", "promotion_intent", "NOT_PROMOTED"),
					resource.TestCheckResourceAttr("apple_win_back_offer.come_back", "prices.#", "1"),
				),
			},
			{
				ResourceName: "apple_win_back_offer.come_back", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return subID + "/" + offerID, nil },
			},
			{
				// Eligibility and dates change in place; removed values clear.
				Config:           locals + winBackConfig("HIGH", `promotion_intent = "USE_AUTO_GENERATED_ASSETS"`),
				ConfigPlanChecks: expectAction("apple_win_back_offer.come_back", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_win_back_offer.come_back", "priority", "HIGH"),
					resource.TestCheckNoResourceAttr("apple_win_back_offer.come_back", "end_date"),
					resource.TestCheckNoResourceAttr("apple_win_back_offer.come_back", "wait_between_offers_months"),
				),
			},
			{
				// New prices replace the offer.
				Config:           locals + regexp.MustCompile(`USA-2.99`).ReplaceAllString(winBackConfig("HIGH", `promotion_intent = "USE_AUTO_GENERATED_ASSETS"`), "USA-1.99"),
				ConfigPlanChecks: expectAction("apple_win_back_offer.come_back", plancheck.ResourceActionDestroyBeforeCreate),
			},
		},
	})
}
