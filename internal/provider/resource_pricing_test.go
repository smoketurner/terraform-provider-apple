package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccPricingDataSources(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	env.run(resource.TestCase{
		Steps: []resource.TestStep{{
			Config: locals + `
data "apple_territories" "all" {}

data "apple_app_price_points" "usa" {
  app_id          = local.app_id
  territories     = ["USA"]
  customer_prices = ["0.99"]
}

data "apple_app_price_point_equalizations" "usa" {
  price_point_id = data.apple_app_price_points.usa.price_points[0].id
  territories    = ["GBR", "JPN"]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.apple_territories.all", "territories.#", "5"),
				resource.TestCheckTypeSetElemAttr("data.apple_territories.all", "ids.*", "USA"),
				resource.TestCheckResourceAttr("data.apple_territories.all", "territories.4.currency", "USD"),
				resource.TestCheckResourceAttr("data.apple_app_price_points.usa", "price_points.#", "1"),
				resource.TestCheckResourceAttr("data.apple_app_price_points.usa", "price_points.0.id", appID+"-USA-1"),
				resource.TestCheckResourceAttr("data.apple_app_price_points.usa", "price_points.0.territory", "USA"),
				resource.TestCheckResourceAttr("data.apple_app_price_points.usa", "price_points.0.proceeds", "0.69"),
				resource.TestCheckResourceAttr("data.apple_app_price_point_equalizations.usa", "price_points.#", "2"),
				resource.TestCheckResourceAttr("data.apple_app_price_point_equalizations.usa", "price_points.0.territory", "GBR"),
				resource.TestCheckResourceAttr("data.apple_app_price_point_equalizations.usa", "price_points.0.customer_price", "0.99"),
			),
		}},
	})
}

func priceScheduleConfig(base, prices string) string {
	return `
resource "apple_app_price_schedule" "app" {
  app_id         = local.app_id
  base_territory = "` + base + `"
  manual_prices  = [` + prices + `]
}
`
}

func TestAccAppPriceSchedule(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	point := func(territory string, tier int) string { return fmt.Sprintf("%s-%s-%d", appID, territory, tier) }
	price := func(id, dates string) string { return `{ price_point_id = "` + id + `"` + dates + ` }` }
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + priceScheduleConfig("USA", price(point("USA", 1), `, start_date = "Dec 1"`)),
				ExpectError: regexp.MustCompile(`must be a date such as`),
			},
			{
				Config:      locals + priceScheduleConfig("USA", price(point("GBR", 1), "")),
				ExpectError: regexp.MustCompile(`The base territory USA needs a manual price`),
			},
			{
				Config:      locals + priceScheduleConfig("USA", price(point("USA", 1), "")+", "+price(point("USA", 2), "")),
				ExpectError: regexp.MustCompile(`only one price without dates`),
			},
			{
				Config: locals + priceScheduleConfig("USA", price(point("USA", 1), "")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_price_schedule.app", "manual_prices.#", "1"),
					resource.TestCheckResourceAttr("apple_app_price_schedule.app", "id", appID),
				),
			},
			{ResourceName: "apple_app_price_schedule.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				// A scheduled price change, and a manual price in Japan.
				Config: locals + priceScheduleConfig("USA", price(point("USA", 1), `, end_date = "2099-01-31"`)+", "+
					price(point("USA", 2), `, start_date = "2099-02-01"`)+", "+price(point("JPN", 3), "")),
				ConfigPlanChecks: expectAction("apple_app_price_schedule.app", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_price_schedule.app", "manual_prices.#", "3"),
					resource.TestCheckTypeSetElemNestedAttrs("apple_app_price_schedule.app", "manual_prices.*", map[string]string{
						"price_point_id": point("USA", 2), "start_date": "2099-02-01",
					}),
				),
			},
			{
				Config: locals + priceScheduleConfig("GBR", price(point("GBR", 2), "")),
				Check:  resource.TestCheckResourceAttr("apple_app_price_schedule.app", "base_territory", "GBR"),
			},
		},
	})
}

func availabilityConfig(newTerritories bool, territories string) string {
	return fmt.Sprintf(`
resource "apple_app_availability" "app" {
  app_id                       = local.app_id
  available_in_new_territories = %t
}
`, newTerritories) + territories
}

func territoryConfig(territory, extra string) string {
	return `
resource "apple_app_territory_availability" "` + territory + `" {
  app_id    = apple_app_availability.app.app_id
  territory = "` + territory + `"
` + extra + `
}
`
}

// territoryAvailable reports a territory's availability through the API.
func (e *testEnv) checkTerritory(appID, territory string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		found, err := findTerritoryAvailabilities(context.Background(), e.client, appID, territory)
		if err != nil {
			return err
		}
		if got := *found[0].Attributes.Available; got != want {
			return fmt.Errorf("%s available = %t, want %t", territory, got, want)
		}
		return nil
	}
}

func TestAccAppAvailability(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	env.run(resource.TestCase{
		CheckDestroy: func(s *terraform.State) error {
			// The availability stays; destroyed territories are unavailable.
			return env.checkTerritory(appID, "USA", false)(s)
		},
		Steps: []resource.TestStep{
			{
				// Without an availability, territories can't be set.
				Config: locals + `
resource "apple_app_territory_availability" "USA" {
  app_id    = local.app_id
  territory = "USA"
  available = true
}
`,
				ExpectError: regexp.MustCompile(`the app has no availability`),
			},
			{
				Config: locals + availabilityConfig(true, territoryConfig("USA", "available = true")+
					territoryConfig("CAN", `available = true
release_date = "2099-06-01"
pre_order_enabled = true`)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_availability.app", "id", appID),
					resource.TestCheckResourceAttr("apple_app_territory_availability.USA", "content_statuses.0", "AVAILABLE"),
					resource.TestCheckResourceAttr("apple_app_territory_availability.CAN", "content_statuses.0", "AVAILABLE_FOR_PREORDER"),
					resource.TestCheckResourceAttrSet("apple_app_territory_availability.CAN", "pre_order_publish_date"),
					// Territories without a resource are unavailable.
					env.checkTerritory(appID, "GBR", false),
				),
			},
			{ResourceName: "apple_app_availability.app", ImportState: true, ImportStateVerify: true, ImportStateId: appID, ImportStateVerifyIdentifierAttribute: "app_id"},
			{
				ResourceName: "apple_app_territory_availability.CAN", ImportState: true, ImportStateVerify: true,
				ImportStateId: appID + "/CAN", ImportStateVerifyIdentifierAttribute: "territory",
			},
			{
				Config: locals + availabilityConfig(true, territoryConfig("USA", "available = true")+
					territoryConfig("CAN", `available = true
release_date = "2020-01-01"
pre_order_enabled = true`)),
				ExpectError: regexp.MustCompile(`release date in the future`),
			},
			{
				// Changing the availability keeps the territories; removing the
				// pre-order clears its release date.
				Config:           locals + availabilityConfig(false, territoryConfig("USA", "available = true")+territoryConfig("CAN", "available = true")),
				ConfigPlanChecks: expectAction("apple_app_availability.app", plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_app_availability.app", "available_in_new_territories", "false"),
					resource.TestCheckNoResourceAttr("apple_app_territory_availability.CAN", "release_date"),
					env.checkTerritory(appID, "USA", true),
				),
			},
			{
				// Destroying a territory's resource makes the app unavailable
				// there.
				Config: locals + availabilityConfig(false, territoryConfig("USA", "available = true")),
				Check:  env.checkTerritory(appID, "CAN", false),
			},
		},
	})
}

func TestAccEndPreOrdersAction(t *testing.T) {
	env := newTestEnv(t)
	appID, locals := env.testflightApp()
	action := `
action "apple_end_pre_orders" "usa" {
  config {
    app_id      = apple_app_availability.app.app_id
    territories = ["USA"]
  }
}
`
	cfg := locals + action + availabilityConfig(true, territoryConfig("USA", `available = true
release_date = "2099-06-01"
pre_order_enabled = true`))
	released := locals + action + availabilityConfig(true, territoryConfig("USA", "available = true"))
	env.actionCase(
		resource.TestStep{Config: cfg},
		resource.TestStep{
			// Ending the pre-order releases the app, which the resource then
			// shows as a change.
			Config: cfg + triggerAction("1", "apple_end_pre_orders.usa"),
			Check: func(*terraform.State) error {
				found, err := findTerritoryAvailabilities(context.Background(), env.client, appID, "USA")
				if err != nil {
					return err
				}
				if a := found[0].Attributes; *a.PreOrderEnabled || a.ReleaseDate != nil {
					return fmt.Errorf("pre-order still enabled: %v, %v", *a.PreOrderEnabled, a.ReleaseDate)
				}
				return nil
			},
			ExpectNonEmptyPlan: true,
		},
		resource.TestStep{
			// Without a pre-order, there is nothing to end.
			Config:      released + triggerAction("2", "apple_end_pre_orders.usa"),
			ExpectError: regexp.MustCompile(`has no\s+pre-order`),
		},
		resource.TestStep{Config: released},
	)
}
