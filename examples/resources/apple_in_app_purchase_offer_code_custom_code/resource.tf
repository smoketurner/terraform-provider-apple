resource "apple_in_app_purchase_offer_code_custom_code" "half" {
  offer_code_id   = apple_in_app_purchase_offer_code.half.id
  custom_code     = "SPRING26"
  number_of_codes = 1000
  expiration_date = "2026-06-30"
}
