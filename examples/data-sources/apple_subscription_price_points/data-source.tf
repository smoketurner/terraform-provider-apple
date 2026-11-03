data "apple_subscription_price_points" "usd" {
  subscription_id = apple_subscription.monthly.id
  territories     = ["USA"]
}
