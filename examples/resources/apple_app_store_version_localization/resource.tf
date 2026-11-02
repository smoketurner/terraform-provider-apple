resource "apple_app_store_version_localization" "en_us" {
  version_id  = apple_app_store_version.ios.id
  locale      = "en-US"
  description = "Example keeps your examples in one place."
  keywords    = "example,demo"
  support_url = "https://example.com/support"
  whats_new   = "Bug fixes and performance improvements."
}
