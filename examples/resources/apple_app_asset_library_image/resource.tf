resource "apple_app_asset_library_image" "home" {
  app_id         = data.apple_app.example.id
  file           = "${path.module}/screenshots/home.png"
  category       = "APP_SCREENSHOTS_AND_PREVIEWS"
  reference_name = "Home screen"
}
