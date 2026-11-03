action "apple_submit_subscription" "monthly" {
  config {
    app_id          = data.apple_app.example.id
    subscription_id = apple_subscription.monthly.id
  }
}
