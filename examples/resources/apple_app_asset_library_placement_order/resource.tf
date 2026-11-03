resource "apple_app_asset_library_placement_order" "iphone" {
  app_store_version_localization_id = apple_app_store_version_localization.en_US.id
  placement_group                   = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"
  placement_ids = [
    apple_app_asset_library_placement.tour.id,
    apple_app_asset_library_placement.home.id,
  ]
}
