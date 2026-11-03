resource "apple_promoted_purchase_order" "app" {
  app_id = data.apple_app.example.id
  promoted_purchase_ids = [
    apple_promoted_purchase.pro.id,
    apple_promoted_purchase.coins.id,
  ]
}
