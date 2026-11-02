# Every enabled iOS device, for a development profile.
data "apple_devices" "ios" {
  platforms = ["IOS"]
  statuses  = ["ENABLED"]
}

resource "apple_profile" "development" {
  name            = "Example Development"
  profile_type    = "IOS_APP_DEVELOPMENT"
  bundle_id       = apple_bundle_id.app.id
  certificate_ids = [apple_certificate.development.id]
  device_ids      = data.apple_devices.ios.devices[*].id
}
