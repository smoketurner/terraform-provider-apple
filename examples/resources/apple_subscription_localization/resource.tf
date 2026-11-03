resource "apple_subscription_localization" "en_us" {
  subscription_id = apple_subscription.monthly.id
  locale          = "en-US"
  name            = "Pro, Monthly"
  description     = "Everything in Example, billed monthly."
}
