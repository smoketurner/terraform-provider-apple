resource "apple_app_clip_advanced_experience" "cafe" {
  app_clip_id       = data.apple_app_clips.app.app_clips[0].id
  link              = "https://example.com/order/cafe"
  action            = "OPEN"
  business_category = "FOOD_AND_DRINK"
  is_powered_by     = false
  header_image_file = "${path.module}/cafe.png"

  default_language = "EN"
  localizations = [
    { language = "EN", title = "Example Cafe", subtitle = "Order ahead and skip the line." },
    { language = "FR", title = "Café Exemple", subtitle = "Commandez à l'avance." },
  ]

  place = {
    names          = ["Example Cafe"]
    map_action     = "RESTAURANT_ORDER_FOOD"
    relationship   = "OWNER"
    home_page      = "https://example.com"
    street_address = ["1 Example Way"]
    locality       = "Cupertino"
    state_province = "CA"
    postal_code    = "95014"
    country_code   = "US"
    latitude       = 37.3349
    longitude      = -122.009
  }
}
