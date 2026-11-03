data "apple_in_app_purchase_price_points" "usd" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  territories        = ["USA"]
  customer_prices    = ["0.99"]
}

resource "apple_in_app_purchase_price_schedule" "coins" {
  in_app_purchase_id = apple_in_app_purchase.coins.id
  base_territory     = "USA"
  manual_prices = [
    { price_point_id = data.apple_in_app_purchase_price_points.usd.price_points[0].id },
  ]
}
