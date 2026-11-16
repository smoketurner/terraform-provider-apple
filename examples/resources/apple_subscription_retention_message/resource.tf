resource "apple_subscription_retention_message" "monthly" {
  subscription_id      = apple_subscription.monthly.id
  retention_message_id = apple_retention_message.stay.id
}
