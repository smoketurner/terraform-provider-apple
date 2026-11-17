resource "apple_game_center_leaderboard_localization" "en_US" {
  leaderboard_id            = apple_game_center_leaderboard.high_scores.id
  locale                    = "en-US"
  name                      = "High Scores"
  formatter_suffix          = " points"
  formatter_suffix_singular = " point"
}
