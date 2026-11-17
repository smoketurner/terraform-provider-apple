resource "apple_game_center_activity_leaderboard" "example" {
  activity_id    = apple_game_center_activity.example.id
  leaderboard_id = apple_game_center_leaderboard.example.id
}
