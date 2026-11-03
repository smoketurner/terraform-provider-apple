resource "apple_in_app_purchase_review_screenshot" "coins" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  file               = "${path.module}/review/coins-store.png"
}
