resource "apple_app_screenshot_set" "iphone" {
  localization_id = apple_app_store_version_localization.en_us.id
  display_type    = "APP_IPHONE_67"
  files = [
    "${path.module}/screenshots/en-US/1-home.png",
    "${path.module}/screenshots/en-US/2-editor.png",
    "${path.module}/screenshots/en-US/3-share.png",
  ]
}
