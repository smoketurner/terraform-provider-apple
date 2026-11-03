resource "apple_in_app_purchase_offer_code" "half" {
  in_app_purchase_id     = apple_in_app_purchase.coins.id
  name                   = "Half price"
  customer_eligibilities = ["NON_SPENDER"]
  prices = [
    { territory = "USA", price_point_id = data.apple_in_app_purchase_price_points.usd.price_points[0].id },
  ]
}
