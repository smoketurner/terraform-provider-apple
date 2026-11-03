data "apple_app_search_keywords" "en_us" {
  app_id   = data.apple_app.example.id
  platform = "IOS"
  locale   = "en-US"
}

resource "apple_app_store_version_localization_search_keyword" "en_us" {
  for_each        = data.apple_app_search_keywords.en_us.ids
  localization_id = apple_app_store_version_localization.en_us.id
  keyword_id      = each.key
}
