resource "apple_game_center_leaderboard_set_localization" "en_US" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  locale             = "en-US"
  name               = "Seasons"
}
