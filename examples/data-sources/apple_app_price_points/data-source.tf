data "apple_app_price_points" "usd" {
  app_id          = data.apple_app.example.id
  territories     = ["USA"]
  customer_prices = ["0.99"]
}
