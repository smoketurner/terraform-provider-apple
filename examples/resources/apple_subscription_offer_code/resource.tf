resource "apple_subscription_offer_code" "spring" {
  subscription_id        = apple_subscription.monthly.id
  name                   = "Spring"
  customer_eligibilities = ["NEW", "EXPIRED"]
  offer_eligibility      = "STACK_WITH_INTRO_OFFERS"
  offer_mode             = "FREE_TRIAL"
  duration               = "ONE_MONTH"
  number_of_periods      = 1
}
