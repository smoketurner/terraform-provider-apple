data "apple_subscription_price_points" "monthly" {
  subscription_id = apple_subscription.monthly.id
}

locals {
  monthly_points = { for p in data.apple_subscription_price_points.monthly.price_points : "${p.territory}-${p.customer_price}" => p.id }
}

resource "apple_retention_offer" "stay" {
  subscription_id            = apple_subscription.monthly.id
  reference_name             = "Stay one month"
  offer_id                   = "stay_one_month"
  offer_mode                 = "PAY_UP_FRONT"
  duration                   = "ONE_MONTH"
  period_count               = 1
  price_point_ids            = [local.monthly_points["USA-2.99"], local.monthly_points["GBR-2.49"]]
  paid_subscription_months   = 3
  wait_between_offers_months = 6
}
