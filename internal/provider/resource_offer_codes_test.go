package provider

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestParseCodes(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want []string
	}{
		{"header and codes", "Code\nABC1\nABC2\n", []string{"ABC1", "ABC2"}},
		{"lowercase header with more columns", "code,status\nABC1,UNUSED\n", []string{"ABC1"}},
		{"no header", "ABC1\nABC2", []string{"ABC1", "ABC2"}},
		{"blank lines", "Code\n\nABC1\n", []string{"ABC1"}},
		{"empty", "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCodes([]byte(tt.csv))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCodes = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := parseCodes([]byte("\"unterminated\n")); err == nil {
		t.Error("parseCodes accepted malformed CSV")
	}
}

func subscriptionOfferCodeConfig(extra string) string {
	return subscriptionPricesConfig("") + `
resource "apple_subscription_offer_code" "spring" {
  subscription_id        = apple_subscription.monthly.id
  name                   = "Spring"
  customer_eligibilities = ["NEW", "EXPIRED"]
  offer_eligibility      = "STACK_WITH_INTRO_OFFERS"
  offer_mode             = "PAY_AS_YOU_GO"
  duration               = "ONE_MONTH"
  number_of_periods      = 3
  prices                 = [` + offerPrice("USA", "0.99") + `]
` + extra + `
}
`
}

func TestAccSubscriptionOfferCode(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	codes := `
resource "apple_subscription_offer_code_custom_code" "spring" {
  offer_code_id   = apple_subscription_offer_code.spring.id
  custom_code     = "SPRING26"
  number_of_codes = 500
}

resource "apple_subscription_offer_code_one_time_use_codes" "batch" {
  offer_code_id   = apple_subscription_offer_code.spring.id
  number_of_codes = 3
  expiration_date = "2099-06-30"
}

data "apple_subscription_offer_code_one_time_use_code_values" "batch" {
  batch_id = apple_subscription_offer_code_one_time_use_codes.batch.id
}
`
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      locals + regexp.MustCompile(`\["NEW", "EXPIRED"\]`).ReplaceAllString(subscriptionOfferCodeConfig(""), `["NEW", "LAPSED"]`),
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			{
				Config: locals + subscriptionOfferCodeConfig("") + codes,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apple_subscription_offer_code.spring", "active", "true"),
					resource.TestCheckResourceAttr("apple_subscription_offer_code.spring", "auto_renew_enabled", "true"),
					resource.TestCheckResourceAttr("apple_subscription_offer_code.spring", "prices.#", "1"),
					resource.TestCheckResourceAttr("apple_subscription_offer_code_custom_code.spring", "custom_code", "SPRING26"),
					resource.TestCheckResourceAttr("apple_subscription_offer_code_one_time_use_codes.batch", "environment", "PRODUCTION"),
					resource.TestCheckResourceAttr("data.apple_subscription_offer_code_one_time_use_code_values.batch", "codes.#", "3"),
				),
			},
			{ResourceName: "apple_subscription_offer_code.spring", ImportState: true, ImportStateVerify: true},
			{
				ResourceName: "apple_subscription_offer_code_custom_code.spring", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["apple_subscription_offer_code.spring"].Primary.ID + "/" + s.RootModule().Resources["apple_subscription_offer_code_custom_code.spring"].Primary.ID, nil
				},
			},
			{
				ResourceName: "apple_subscription_offer_code_one_time_use_codes.batch", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["apple_subscription_offer_code.spring"].Primary.ID + "/" + s.RootModule().Resources["apple_subscription_offer_code_one_time_use_codes.batch"].Primary.ID, nil
				},
			},
			{
				// A custom code can't be reused.
				Config: locals + subscriptionOfferCodeConfig("") + codes + `
resource "apple_subscription_offer_code_custom_code" "again" {
  offer_code_id   = apple_subscription_offer_code.spring.id
  custom_code     = "spring26"
  number_of_codes = 1
}
`,
				ExpectError: regexp.MustCompile(`is already\s+used`),
			},
			{
				// Deactivating changes the codes in place.
				Config:           locals + subscriptionOfferCodeConfig("") + regexp.MustCompile(`number_of_codes = 500`).ReplaceAllString(codes, "number_of_codes = 500\n  active = false"),
				ConfigPlanChecks: expectAction("apple_subscription_offer_code_custom_code.spring", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_subscription_offer_code_custom_code.spring", "active", "false"),
			},
			{
				// Reactivating creates a new custom code.
				Config:           locals + subscriptionOfferCodeConfig("") + codes,
				ConfigPlanChecks: expectAction("apple_subscription_offer_code_custom_code.spring", plancheck.ResourceActionDestroyBeforeCreate),
				ExpectError:      regexp.MustCompile(`is already\s+used`),
			},
			{
				// Destroying the codes deactivates them.
				Config: locals + subscriptionOfferCodeConfig(`active = false`),
				Check:  resource.TestCheckResourceAttr("apple_subscription_offer_code.spring", "active", "false"),
			},
		},
	})
}

func TestAccInAppPurchaseOfferCode(t *testing.T) {
	env := newTestEnv(t)
	_, locals := env.testflightApp()
	var iapID string
	cfg := func(extra string) string {
		return locals + iapScheduleConfig("USA", `{ price_point_id = local.points["USA-1.99"] }`) + `
resource "apple_in_app_purchase_offer_code" "half" {
  in_app_purchase_id     = apple_in_app_purchase.coins.id
  name                   = "Half price"
  customer_eligibilities = ["NON_SPENDER", "CHURNED_SPENDER"]
  prices                 = [{ territory = "USA", price_point_id = local.points["USA-0.99"] }]
` + extra + `
}

resource "apple_in_app_purchase_offer_code_one_time_use_codes" "batch" {
  offer_code_id   = apple_in_app_purchase_offer_code.half.id
  number_of_codes = 2
  expiration_date = "2099-06-30"
  environment     = "SANDBOX"
}

resource "apple_in_app_purchase_offer_code_custom_code" "half" {
  offer_code_id   = apple_in_app_purchase_offer_code.half.id
  custom_code     = "HALF26"
  number_of_codes = 10
  expiration_date = "2099-06-30"
}

data "apple_in_app_purchase_offer_code_one_time_use_code_values" "batch" {
  batch_id = apple_in_app_purchase_offer_code_one_time_use_codes.batch.id
}
`
	}
	env.run(resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					captureAttr("apple_in_app_purchase.coins", "id", &iapID),
					resource.TestCheckResourceAttr("apple_in_app_purchase_offer_code.half", "customer_eligibilities.#", "2"),
					resource.TestCheckResourceAttr("apple_in_app_purchase_offer_code_one_time_use_codes.batch", "environment", "SANDBOX"),
					resource.TestCheckResourceAttr("apple_in_app_purchase_offer_code_custom_code.half", "expiration_date", "2099-06-30"),
					func(s *terraform.State) error {
						attrs := s.RootModule().Resources["data.apple_in_app_purchase_offer_code_one_time_use_code_values.batch"].Primary.Attributes
						if n, _ := strconv.Atoi(attrs["codes.#"]); n != 2 || attrs["codes.0"] == "" {
							return fmt.Errorf("codes = %v", attrs)
						}
						return nil
					},
				),
			},
			{
				ResourceName: "apple_in_app_purchase_offer_code.half", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return iapID + "/" + s.RootModule().Resources["apple_in_app_purchase_offer_code.half"].Primary.ID, nil
				},
			},
			{
				Config:           cfg("active = false"),
				ConfigPlanChecks: expectAction("apple_in_app_purchase_offer_code.half", plancheck.ResourceActionUpdate),
				Check:            resource.TestCheckResourceAttr("apple_in_app_purchase_offer_code.half", "active", "false"),
			},
		},
	})
}
