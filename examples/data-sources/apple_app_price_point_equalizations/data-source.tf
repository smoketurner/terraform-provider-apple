data "apple_app_price_point_equalizations" "usd" {
  price_point_id = data.apple_app_price_points.usd.price_points[0].id
  territories    = ["GBR", "JPN"]
}
