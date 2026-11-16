resource "apple_app_store_version_experiment_treatment_localization" "en_US" {
  treatment_id = apple_app_store_version_experiment_treatment.orange.id
  locale       = "en-US"
}

# A screenshot only this treatment shows.
resource "apple_app_asset_library_placement" "orange" {
  image_id                             = apple_app_asset_library_image.orange.id
  experiment_treatment_localization_id = apple_app_store_version_experiment_treatment_localization.en_US.id
  placement_type                       = "APP_SCREENSHOT"
  placement_group                      = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"
}
