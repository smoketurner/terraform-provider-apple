resource "apple_accessibility_declaration" "iphone" {
  app_id                  = data.apple_app.example.id
  device_family           = "IPHONE"
  supports_voiceover      = true
  supports_larger_text    = true
  supports_dark_interface = true
  publish                 = true
}
