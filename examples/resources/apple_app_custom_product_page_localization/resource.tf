resource "apple_app_custom_product_page_localization" "en_US" {
  custom_product_page_id = apple_app_custom_product_page.summer.id
  locale                 = "en-US"
  promotional_text       = "Play the summer cup."
}

# A screenshot on the page.
resource "apple_app_asset_library_placement" "summer" {
  image_id                            = apple_app_asset_library_image.summer.id
  custom_product_page_localization_id = apple_app_custom_product_page_localization.en_US.id
  placement_type                      = "APP_SCREENSHOT"
  placement_group                     = "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE"
}
