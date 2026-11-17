resource "apple_game_center_leaderboard_set_order" "example" {
  game_center_detail_id = apple_game_center_detail.example.id
  leaderboard_set_ids   = [apple_game_center_leaderboard_set.seasons.id, apple_game_center_leaderboard_set.modes.id]
}
