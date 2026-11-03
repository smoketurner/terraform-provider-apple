resource "apple_webhook" "builds" {
  app_id      = data.apple_app.example.id
  name        = "Build notifications"
  url         = "https://hooks.example.com/app-store-connect"
  event_types = ["BUILD_UPLOAD_STATE_UPDATED", "APP_STORE_VERSION_APP_VERSION_STATE_UPDATED"]
  secret      = var.webhook_secret
}
