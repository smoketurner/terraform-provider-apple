resource "apple_subscription_group" "pro" {
  app_id         = data.apple_app.example.id
  reference_name = "Pro"
}
