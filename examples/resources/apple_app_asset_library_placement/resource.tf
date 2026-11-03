resource "apple_app_asset_library_placement" "home" {
  image_id                          = apple_app_asset_library_image.home.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "APP_SCREENSHOT"
  placement_group                   = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"
}

resource "apple_app_asset_library_placement" "tour" {
  video_id                          = apple_app_asset_library_video.tour.id
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_type                    = "APP_PREVIEW"
  placement_group                   = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"
}
