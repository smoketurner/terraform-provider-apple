resource "apple_app_info_localization" "en_us" {
  app_id             = data.apple_app.example.id
  locale             = "en-US"
  name               = "Example"
  subtitle           = "Examples, fast"
  privacy_policy_url = "https://example.com/privacy"
}
