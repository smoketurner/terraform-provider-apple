resource "apple_game_center_leaderboard_order" "example" {
  game_center_detail_id = apple_game_center_detail.example.id
  leaderboard_ids       = [apple_game_center_leaderboard.high_scores.id, apple_game_center_leaderboard.weekly.id]
}
