data "apple_in_app_purchase_price_points" "usd" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  territories        = ["USA"]
}
