data "apple_app_search_keywords" "en_US" {
  app_id   = data.apple_app.example.id
  platform = "IOS"
  locale   = "en-US"
}

resource "apple_app_custom_product_page_localization_search_keyword" "summer" {
  for_each        = data.apple_app_search_keywords.en_US.ids
  localization_id = apple_app_custom_product_page_localization.en_US.id
  keyword_id      = each.value
}
