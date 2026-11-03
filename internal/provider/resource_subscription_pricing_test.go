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
	"github.com/smoketurner/terraform-provider-apple/internal/asc/gen"
)

// subscriptionPricesConfig adds a price points lookup; prices refer to them
// as local.sub_points["<territory>-<customer price>"].
func subscriptionPricesConfig(prices string) string {
	return subscriptionConfig("") + `
data "apple_subscription_price_points" "all" {
  subscription_id = apple_subscription.monthly.id
}

locals {
  sub_points = { for p in data.apple_subscription_price_points.all.price_points : "${p.territory}-${p.customer_price}" => p.id }
}
` + prices
}

// subscriptionPrice sets a price in the United States.
func subscriptionPrice(name, price, extra string) string {
	return `
resource "apple_subscription_price" "` + name + `" {
  subscription_id = apple_subscription.monthly.id
  price_point_id  = local.sub_points["USA-` + price + `"]
` + extra + `
}
`
}

func (e *testEnv) subscriptionPrices(subID string) ([]gen.SubscriptionPrice, error) {
	out, err := e.client.SubscriptionsPricesGetToManyRelatedWithResponse(context.Background(), subID, nil)
	if err := asc.Err(out, err); err != nil {
		return nil, err
	}
	return out.JSON200.Data, nil
}

func TestAccSubscriptionPrice(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var subID, scheduled string
	current := subscriptionPrice("usa", "4.99", "")
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + subscriptionPricesConfig(current) + `
data "apple_subscription_price_point_equalizations" "usa" {
  price_point_id = local.sub_points["USA-4.99"]
  territories    = ["GBR"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription.monthly", "id", &subID),
					resource.TestCheckResourceAttr("apple_subscription_price.usa", "territory", "USA"),
					resource.TestCheckResourceAttr("apple_subscription_price.usa", "plan_type", "UPFRONT"),
					resource.TestCheckResourceAttr("data.apple_subscription_price_point_equalizations.usa", "price_points.0.customer_price", "4.99"),
				),
			},
			{
				ResourceName: "apple_subscription_price.usa", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return subID + "/" + s.RootModule().Resources["apple_subscription_price.usa"].Primary.ID, nil
				},
			},
			{
				Config:      locals + subscriptionPricesConfig(current+subscriptionPrice("dup", "2.99", "")),
				ExpectError: regexp.MustCompile(`already has a price in USA starting\s+then`),
			},
			{
				// A scheduled increase that keeps existing subscribers' price.
				Config: locals + subscriptionPricesConfig(current+subscriptionPrice("increase", "2.99", `start_date = "2099-01-01"
preserve_current_price = true`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription_price.increase", "id", &scheduled),
					resource.TestCheckResourceAttr("apple_subscription_price.increase", "preserve_current_price", "true"),
				),
			},
			{
				// Changing the date replaces the scheduled price.
				Config: locals + subscriptionPricesConfig(current+subscriptionPrice("increase", "2.99", `start_date = "2099-02-01"
preserve_current_price = true`)),
				ConfigPlanChecks: expectAction("apple_subscription_price.increase", plancheck.ResourceActionDestroyBeforeCreate),
				Check: func(*terraform.State) error {
					prices, err := env.subscriptionPrices(subID)
					if err != nil {
						return err
					}
					for _, p := range prices {
						if p.Id == scheduled {
							return fmt.Errorf("the replaced price %s still exists", scheduled)
						}
					}
					if len(prices) != 2 {
						return fmt.Errorf("prices = %d, want 2", len(prices))
					}
					return nil
				},
			},
			{
				// Removing the price resources deletes the scheduled price;
				// the price in effect stays.
				Config: locals + subscriptionPricesConfig(""),
				Check: func(*terraform.State) error {
					prices, err := env.subscriptionPrices(subID)
					if err != nil {
						return err
					}
					if len(prices) != 1 || prices[0].Attributes.StartDate != nil {
						return fmt.Errorf("prices after removal = %v, want only the one in effect", prices)
					}
					return nil
				},
			},
		},
	})
}

func planAvailabilityConfig(newTerritories bool, territories string) string {
	return subscriptionConfig("") + fmt.Sprintf(`
resource "apple_subscription_plan_availability" "upfront" {
  subscription_id              = apple_subscription.monthly.id
  plan_type                    = "UPFRONT"
  available_in_new_territories = %t
  territories                  = [%s]
}
`, newTerritories, territories)
}

func TestAccSubscriptionPlanAvailability(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var subID string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + planAvailabilityConfig(true, `"USA", "CAN"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_subscription.monthly", "id", &subID),
					resource.TestCheckResourceAttr("apple_subscription_plan_availability.upfront", "territories.#", "2"),
				),
			},
			{
				ResourceName: "apple_subscription_plan_availability.upfront", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return subID + "/UPFRONT", nil },
				ImportStateVerifyIdentifierAttribute: "plan_type",
			},
			{
				Config:      locals + planAvailabilityConfig(true, `"USA", "ATA"`),
				ExpectError: regexp.MustCompile(`There is no territories with id\s+'ATA'`),
			},
			{
				Config:           locals + planAvailabilityConfig(false, `"JPN"`),
				ConfigPlanChecks: expectAction("apple_subscription_plan_availability.upfront", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_subscription_plan_availability.upfront", "territories.#", "1"),
					resource.TestCheckTypeSetElemAttr("apple_subscription_plan_availability.upfront", "territories.*", "JPN"),
					resource.TestCheckResourceAttr("apple_subscription_plan_availability.upfront", "available_in_new_territories", "false"),
				),
			},
			{
				Config: locals + planAvailabilityConfig(false, ""),
				Check:  resource.TestCheckResourceAttr("apple_subscription_plan_availability.upfront", "territories.#", "0"),
			},
		},
	})
}

func TestAccSubscriptionGracePeriod(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	cfg := func(attrs string) string {
		return locals + `
resource "apple_subscription_grace_period" "app" {
  app_id = local.app_id
` + attrs + `
}
`
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      cfg(`opt_in = true` + "\n" + `duration = "SEVEN_DAYS"`),
				ExpectError: regexp.MustCompile(`duration value must be one of`),
			},
			{
				// Unset options keep App Store Connect's values.
				Config: cfg(`opt_in = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_subscription_grace_period.app", "opt_in", "true"),
					resource.TestCheckResourceAttr("apple_subscription_grace_period.app", "duration", "SIXTEEN_DAYS"),
					resource.TestCheckResourceAttr("apple_subscription_grace_period.app", "renewal_type", "ALL_RENEWALS"),
				),
			},
			{
				ResourceName: "apple_subscription_grace_period.app", ImportState: true, ImportStateVerify: true,
				ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id",
			},
			{
				Config: cfg(`opt_in = true
sandbox_opt_in = true
duration = "TWENTY_EIGHT_DAYS"
renewal_type = "PAID_TO_PAID_ONLY"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_subscription_grace_period.app", "sandbox_opt_in", "true"),
					resource.TestCheckResourceAttr("apple_subscription_grace_period.app", "duration", "TWENTY_EIGHT_DAYS"),
					resource.TestCheckResourceAttr("apple_subscription_grace_period.app", "renewal_type", "PAID_TO_PAID_ONLY"),
				),
			},
		},
	})
}
