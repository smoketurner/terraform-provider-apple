package provider

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// iapScheduleConfig sets a schedule; prices refer to price points as
// local.points["<territory>-<customer price>"].
func iapScheduleConfig(base, prices string) string {
	return iapConfig("") + `
data "apple_in_app_purchase_price_points" "all" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
}

locals {
  points = { for p in data.apple_in_app_purchase_price_points.all.price_points : "${p.territory}-${p.customer_price}" => p.id }
}

resource "apple_in_app_purchase_price_schedule" "coins" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  base_territory     = "` + base + `"
  manual_prices      = [` + prices + `]
}
`
}

func TestAccInAppPurchasePricing(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var id string
	point := func(territory string, tier int) string { return fmt.Sprintf("%s-%s-%d", id, territory, tier) }
	tiers := []string{"0.0", "0.99", "1.99", "2.99", "4.99"}
	price := func(territory string, tier int, dates string) string {
		return `{ price_point_id = local.points["` + territory + "-" + tiers[tier] + `"]` + dates + ` }`
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: locals + iapConfig("") + `
data "apple_in_app_purchase_price_points" "usa" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  territories        = ["USA"]
  customer_prices    = ["1.99"]
}

data "apple_in_app_purchase_price_point_equalizations" "usa" {
  price_point_id = data.apple_in_app_purchase_price_points.usa.price_points[0].id
  territories    = ["JPN"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase.coins", "id", &id),
					resource.TestCheckResourceAttr("data.apple_in_app_purchase_price_points.usa", "price_points.#", "1"),
					func(s *terraform.State) error {
						return resource.TestCheckResourceAttr("data.apple_in_app_purchase_price_points.usa", "price_points.0.id", point("USA", 2))(s)
					},
					resource.TestCheckResourceAttr("data.apple_in_app_purchase_price_point_equalizations.usa", "price_points.0.territory", "JPN"),
					resource.TestCheckResourceAttr("data.apple_in_app_purchase_price_point_equalizations.usa", "price_points.0.customer_price", "1.99"),
				),
			},
			{
				Config:      locals + iapScheduleConfig("USA", price("GBR", 1, "")),
				ExpectError: regexp.MustCompile(`base territory USA needs a manual\s+price`),
			},
			{
				Config: locals + iapScheduleConfig("USA", price("USA", 1, `, end_date = "2099-01-31"`)+", "+price("USA", 2, `, start_date = "2099-02-01"`)),
				Check:  resource.TestCheckResourceAttr("apple_in_app_purchase_price_schedule.coins", "manual_prices.#", "2"),
			},
			{
				ResourceName: "apple_in_app_purchase_price_schedule.coins", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return id, nil },
				ImportStateVerifyIdentifierAttribute: "in_app_purchase_id",
			},
			{
				Config:           locals + iapScheduleConfig("GBR", price("GBR", 3, "")),
				ConfigPlanChecks: expectAction("apple_in_app_purchase_price_schedule.coins", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_in_app_purchase_price_schedule.coins", "base_territory", "GBR"),
			},
		},
	})
}

func iapAvailabilityConfig(newTerritories bool, territories string) string {
	return iapConfig("") + fmt.Sprintf(`
resource "apple_in_app_purchase_availability" "coins" {
  in_app_purchase_id           = apple_in_app_purchase.coins.id
  available_in_new_territories = %t
  territories                  = [%s]
}
`, newTerritories, territories)
}

func TestAccInAppPurchaseAvailability(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var id string
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + iapAvailabilityConfig(true, `"US"`),
				ExpectError: regexp.MustCompile(`must be a territory ID`),
			},
			{
				Config:      locals + iapAvailabilityConfig(true, `"ATA"`),
				ExpectError: regexp.MustCompile(`There is no territories with id\s+'ATA'`),
			},
			{
				Config: locals + iapAvailabilityConfig(true, `"USA", "CAN"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase.coins", "id", &id),
					resource.TestCheckResourceAttr("apple_in_app_purchase_availability.coins", "territories.#", "2"),
				),
			},
			{
				ResourceName: "apple_in_app_purchase_availability.coins", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:                    func(*terraform.State) (string, error) { return id, nil },
				ImportStateVerifyIdentifierAttribute: "in_app_purchase_id",
			},
			{
				// Removing every territory stops sales everywhere.
				Config: locals + iapAvailabilityConfig(false, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_in_app_purchase_availability.coins", "territories.#", "0"),
					resource.TestCheckResourceAttr("apple_in_app_purchase_availability.coins", "available_in_new_territories", "false"),
				),
			},
		},
	})
}

func sandboxTesterConfig(email, extra string) string {
	return `
resource "apple_sandbox_tester" "ann" {
  email = "` + email + `"
` + extra + `
}
`
}

func TestAccSandboxTester(t *testing.T) {
	env := newTestEnv(t)
	testerID := env.fake.AddSandboxTester("ann.sandbox@example.com", "Ann", "Example")
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      sandboxTesterConfig("nobody@example.com", ""),
				ExpectError: regexp.MustCompile(`no sandbox tester has this email`),
			},
			{
				// An unset option keeps App Store Connect's value.
				Config: sandboxTesterConfig("ann.sandbox@example.com", "") + `
data "apple_sandbox_testers" "all" {
  depends_on = [apple_sandbox_tester.ann]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_sandbox_tester.ann", "id", testerID),
					resource.TestCheckResourceAttr("apple_sandbox_tester.ann", "territory", "USA"),
					resource.TestCheckResourceAttr("apple_sandbox_tester.ann", "first_name", "Ann"),
					resource.TestCheckResourceAttr("data.apple_sandbox_testers.all", "sandbox_testers.0.email", "ann.sandbox@example.com"),
				),
			},
			{
				ResourceName: "apple_sandbox_tester.ann", ImportState: true, ImportStateVerify: true,
				ImportStateId: "ann.sandbox@example.com", ImportStateVerifyIdentifierAttribute: "email",
			},
			{
				Config: sandboxTesterConfig("ann.sandbox@example.com", `territory = "JPN"
interrupt_purchases = true
subscription_renewal_rate = "MONTHLY_RENEWAL_EVERY_THREE_MINUTES"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_sandbox_tester.ann", "territory", "JPN"),
					resource.TestCheckResourceAttr("apple_sandbox_tester.ann", "interrupt_purchases", "true"),
					resource.TestCheckResourceAttr("apple_sandbox_tester.ann", "subscription_renewal_rate", "MONTHLY_RENEWAL_EVERY_THREE_MINUTES"),
				),
			},
		},
	})
}

func TestAccClearSandboxHistoryAction(t *testing.T) {
	env := newTestEnv(t)
	testerID := env.fake.AddSandboxTester("ann.sandbox@example.com", "Ann", "Example")
	action := `
action "apple_clear_sandbox_purchase_history" "ann" {
  config {
    sandbox_tester_ids = [apple_sandbox_tester.ann.id]
  }
}
`
	env.actionCase(
		resource.TestStep{Config: sandboxTesterConfig("ann.sandbox@example.com", "") + action},
		resource.TestStep{
			Config: sandboxTesterConfig("ann.sandbox@example.com", "") + action + triggerAction("1", "apple_clear_sandbox_purchase_history.ann"),
			Check: func(*terraform.State) error {
				if got := env.fake.ClearedSandboxTesters(); !slices.Equal(got, []string{testerID}) {
					return fmt.Errorf("cleared testers = %v", got)
				}
				return nil
			},
		},
	)
}
