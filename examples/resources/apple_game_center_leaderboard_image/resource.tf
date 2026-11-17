resource "apple_game_center_leaderboard_image" "en_US" {
  leaderboard_id = apple_game_center_leaderboard.high_scores.id
  locale         = apple_game_center_leaderboard_localization.en_US.locale
  file           = "${path.module}/high-scores.png"
}
