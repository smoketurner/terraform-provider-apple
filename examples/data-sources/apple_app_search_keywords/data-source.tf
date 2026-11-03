data "apple_app_search_keywords" "en_us" {
  app_id   = data.apple_app.example.id
  platform = "IOS"
  locale   = "en-US"
}
