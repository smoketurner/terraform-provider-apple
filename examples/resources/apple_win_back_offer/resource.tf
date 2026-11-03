resource "apple_win_back_offer" "come_back" {
  subscription_id                  = apple_subscription.monthly.id
  reference_name                   = "Come back"
  offer_id                         = "come_back"
  offer_mode                       = "FREE_TRIAL"
  duration                         = "ONE_MONTH"
  period_count                     = 1
  paid_subscription_months         = 3
  months_since_last_subscribed_min = 2
  months_since_last_subscribed_max = 12
  start_date                       = "2026-12-01"
  priority                         = "NORMAL"
}
