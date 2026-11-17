resource "apple_game_center_leaderboard_set_member" "weekly" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  leaderboard_id     = apple_game_center_leaderboard.weekly.id
}
