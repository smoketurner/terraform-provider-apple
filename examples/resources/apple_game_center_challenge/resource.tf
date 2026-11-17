resource "apple_game_center_challenge" "example" {
  game_center_detail_id = apple_game_center_detail.example.id
  reference_name        = "Weekly duel"
  vendor_identifier     = "com.example.weekly_duel"
  challenge_type        = "LEADERBOARD"
  leaderboard_id        = apple_game_center_leaderboard.weekly.id
  repeatable            = true
}
