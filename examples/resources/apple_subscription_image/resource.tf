resource "apple_subscription_image" "monthly" {
  subscription_id = apple_subscription.monthly.id
  file            = "${path.module}/store/pro-1024.png"
}
