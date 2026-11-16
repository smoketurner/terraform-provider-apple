resource "apple_app_event_localization" "en_US" {
  app_event_id      = apple_app_event.summer.id
  locale            = "en-US"
  name              = "Summer Cup"
  short_description = "Compete for the summer title."
  long_description  = "Win matches all month to climb the leaderboard and take the cup."
}

# The image on the event card.
resource "apple_app_asset_library_placement" "card" {
  image_id                  = apple_app_asset_library_image.card.id
  app_event_localization_id = apple_app_event_localization.en_US.id
  placement_type            = "EVENT_CARD_ASSET"
  placement_group           = "DEFAULT_PROFILE"
}
