data "apple_ci_products" "all" {}

data "apple_ci_products" "app" {
  app_id = data.apple_app.example.id
}
