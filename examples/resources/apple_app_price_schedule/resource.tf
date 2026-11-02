data "apple_app_price_points" "usd" {
  app_id          = data.apple_app.example.id
  territories     = ["USA"]
  customer_prices = ["0.99", "1.99"]
}

resource "apple_app_price_schedule" "example" {
  app_id         = data.apple_app.example.id
  base_territory = "USA"
  manual_prices = [
    # 0.99 until the end of the year, then 1.99.
    { price_point_id = data.apple_app_price_points.usd.price_points[0].id, end_date = "2026-12-31" },
    { price_point_id = data.apple_app_price_points.usd.price_points[1].id, start_date = "2027-01-01" },
  ]
}
