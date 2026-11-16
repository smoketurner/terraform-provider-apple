resource "apple_subscription_retention_message_offer" "stay" {
  subscription_id    = apple_subscription.monthly.id
  retention_offer_id = apple_retention_offer.stay.id
}
