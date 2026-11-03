resource "apple_in_app_purchase_image" "coins" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  file               = "${path.module}/store/coins-1024.png"
}
