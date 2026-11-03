data "apple_subscription_price_point_equalizations" "usd" {
  price_point_id = data.apple_subscription_price_points.usd.price_points[0].id
}
