# The image is optional. A retention message placement has no localization.
resource "apple_app_asset_library_placement" "stay" {
  image_id        = apple_app_asset_library_image.stay.id
  placement_type  = "RETENTION_MESSAGE_ASSET"
  placement_group = "DEFAULT_PROFILE"
}

resource "apple_retention_message_version" "first" {
  retention_message_id = apple_retention_message.stay.id
  placement_id         = apple_app_asset_library_placement.stay.id
}
