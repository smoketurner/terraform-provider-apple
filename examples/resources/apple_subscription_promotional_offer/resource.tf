resource "apple_subscription_promotional_offer" "half" {
  subscription_id   = apple_subscription.monthly.id
  name              = "Half off"
  offer_code        = "half_off"
  offer_mode        = "PAY_AS_YOU_GO"
  duration          = "ONE_MONTH"
  number_of_periods = 2
  prices = [
    { territory = "USA", price_point_id = data.apple_subscription_price_points.usd.price_points[0].id },
  ]
}
