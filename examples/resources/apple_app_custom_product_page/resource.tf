resource "apple_app_custom_product_page" "summer" {
  app_id    = data.apple_app.example.id
  name      = "Summer Cup"
  deep_link = "https://example.com/summer"
}
