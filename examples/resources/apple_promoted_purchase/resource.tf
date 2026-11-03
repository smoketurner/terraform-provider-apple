resource "apple_promoted_purchase" "pro" {
  app_id                = data.apple_app.example.id
  subscription_id       = apple_subscription.monthly.id
  visible_for_all_users = true
}
