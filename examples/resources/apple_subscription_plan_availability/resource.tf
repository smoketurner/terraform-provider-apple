data "apple_territories" "all" {}

resource "apple_subscription_plan_availability" "upfront" {
  subscription_id              = apple_subscription.monthly.id
  plan_type                    = "UPFRONT"
  available_in_new_territories = true
  territories                  = data.apple_territories.all.ids
}
