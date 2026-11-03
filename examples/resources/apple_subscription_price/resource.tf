data "apple_subscription_price_points" "usd" {
  subscription_id = apple_subscription.monthly.id
  territories     = ["USA"]
  customer_prices = ["4.99"]
}

resource "apple_subscription_price" "usa" {
  subscription_id = apple_subscription.monthly.id
  price_point_id  = data.apple_subscription_price_points.usd.price_points[0].id
}
