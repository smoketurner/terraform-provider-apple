resource "apple_app_preview_set" "iphone" {
  localization_id = apple_app_store_version_localization.en_us.id
  display_type    = "IPHONE_67"
  files           = ["${path.module}/previews/en-US/tour.mov"]
}
