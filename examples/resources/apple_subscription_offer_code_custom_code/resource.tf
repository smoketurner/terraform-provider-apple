resource "apple_subscription_offer_code_custom_code" "spring" {
  offer_code_id   = apple_subscription_offer_code.spring.id
  custom_code     = "SPRING26"
  number_of_codes = 1000
  expiration_date = "2026-06-30"
}
