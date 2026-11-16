resource "apple_game_center_app_version_compatibility" "ios_tv" {
  game_center_app_version_id            = apple_game_center_app_version.ios.id
  compatible_game_center_app_version_id = apple_game_center_app_version.tv.id
}
