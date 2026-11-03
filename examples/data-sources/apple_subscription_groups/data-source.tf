data "apple_subscription_groups" "app" {
  app_id = data.apple_app.example.id
}
