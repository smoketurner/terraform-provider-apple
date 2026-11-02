resource "apple_profile" "app_store" {
  name            = "Example App Store"
  profile_type    = "IOS_APP_STORE"
  bundle_id       = apple_bundle_id.app.id
  certificate_ids = [apple_certificate.distribution.id]
}

resource "apple_profile" "development" {
  name            = "Example Development"
  profile_type    = "IOS_APP_DEVELOPMENT"
  bundle_id       = apple_bundle_id.app.id
  certificate_ids = [apple_certificate.development.id]
  device_ids      = [apple_device.test_phone.id]
}

# Write the profile for Xcode or a CI system.
resource "local_sensitive_file" "app_store" {
  content_base64 = apple_profile.app_store.profile_content
  filename       = "${path.module}/Example_App_Store.mobileprovision"
}
