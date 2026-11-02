resource "apple_beta_app_localization" "en" {
  app_id             = data.apple_app.example.id
  locale             = "en-US"
  description        = "Example helps you test examples."
  feedback_email     = "beta@example.com"
  marketing_url      = "https://example.com"
  privacy_policy_url = "https://example.com/privacy"
}
