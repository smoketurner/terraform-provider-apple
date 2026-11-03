resource "apple_subscription_offer_code_one_time_use_codes" "batch" {
  offer_code_id   = apple_subscription_offer_code.spring.id
  number_of_codes = 500
  expiration_date = "2026-06-30"
}
