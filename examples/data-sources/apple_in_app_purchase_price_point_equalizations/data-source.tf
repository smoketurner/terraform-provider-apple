data "apple_in_app_purchase_price_point_equalizations" "usd" {
  price_point_id = data.apple_in_app_purchase_price_points.usd.price_points[0].id
}
