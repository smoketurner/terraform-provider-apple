resource "apple_in_app_purchase_offer_code_one_time_use_codes" "batch" {
  offer_code_id   = apple_in_app_purchase_offer_code.half.id
  number_of_codes = 500
  expiration_date = "2026-06-30"
}
