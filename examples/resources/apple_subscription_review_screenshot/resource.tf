resource "apple_subscription_review_screenshot" "monthly" {
  subscription_id = apple_subscription.monthly.id
  file            = "${path.module}/review/upgrade.png"
}
