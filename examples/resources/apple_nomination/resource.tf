resource "apple_nomination" "launch" {
  name               = "Example 2.0"
  type               = "APP_ENHANCEMENTS"
  description        = "A redesigned editor with real-time collaboration."
  app_ids            = [data.apple_app.example.id]
  publish_start_date = "2026-12-01T16:00:00Z"
  device_families    = ["IPHONE", "IPAD"]
  locales            = ["en-US"]
  submitted          = true
}
