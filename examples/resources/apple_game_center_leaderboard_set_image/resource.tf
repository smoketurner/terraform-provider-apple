resource "apple_game_center_leaderboard_set_image" "en_US" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  locale             = apple_game_center_leaderboard_set_localization.en_US.locale
  file               = "${path.module}/seasons.png"
}
