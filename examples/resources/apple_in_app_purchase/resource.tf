resource "apple_in_app_purchase" "coins" {
  app_id      = data.apple_app.example.id
  product_id  = "com.example.coins100"
  type        = "CONSUMABLE"
  name        = "100 Coins"
  review_note = "Open Store, then tap 100 Coins."
}
