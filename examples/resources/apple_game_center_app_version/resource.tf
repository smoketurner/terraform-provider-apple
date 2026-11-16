resource "apple_game_center_app_version" "example" {
  app_store_version_id = apple_app_store_version.example.id
  enabled              = true

  # Game Center must be on for the app first.
  depends_on = [apple_game_center_detail.example]
}
