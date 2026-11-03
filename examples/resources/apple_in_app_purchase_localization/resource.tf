resource "apple_in_app_purchase_localization" "en_us" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  locale             = "en-US"
  name               = "100 Coins"
  description        = "A pile of coins to spend in the shop."
}
