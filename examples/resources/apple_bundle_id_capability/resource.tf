resource "apple_bundle_id_capability" "push" {
  bundle_id       = apple_bundle_id.app.id
  capability_type = "PUSH_NOTIFICATIONS"
}

resource "apple_bundle_id_capability" "icloud" {
  bundle_id       = apple_bundle_id.app.id
  capability_type = "ICLOUD"
  settings = {
    ICLOUD_VERSION = ["XCODE_6"]
  }
}
