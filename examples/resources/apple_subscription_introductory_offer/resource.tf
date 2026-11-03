resource "apple_subscription_introductory_offer" "trial" {
  subscription_id   = apple_subscription.monthly.id
  territory         = "USA"
  offer_mode        = "FREE_TRIAL"
  duration          = "ONE_WEEK"
  number_of_periods = 1
}
