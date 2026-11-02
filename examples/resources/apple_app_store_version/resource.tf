resource "apple_app_store_version" "ios" {
  app_id         = data.apple_app.example.id
  platform       = "IOS"
  version_string = "1.2.0"
  copyright      = "2026 Example Inc."
  release_type   = "MANUAL"
  build_id       = data.apple_build.latest.id
}
