resource "apple_app_settings" "example" {
  app_id                          = data.apple_app.example.id
  primary_locale                  = "en-US"
  content_rights_declaration      = "DOES_NOT_USE_THIRD_PARTY_CONTENT"
  subscription_status_url         = "https://example.com/app-store/notifications"
  subscription_status_url_version = "V2"
}
