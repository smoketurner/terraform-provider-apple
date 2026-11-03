resource "apple_subscription_grace_period" "app" {
  app_id       = data.apple_app.example.id
  opt_in       = true
  duration     = "SIXTEEN_DAYS"
  renewal_type = "ALL_RENEWALS"
}
