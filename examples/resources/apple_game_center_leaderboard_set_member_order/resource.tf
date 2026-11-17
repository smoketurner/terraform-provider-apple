resource "apple_game_center_leaderboard_set_member_order" "seasons" {
  leaderboard_set_id = apple_game_center_leaderboard_set.seasons.id
  leaderboard_ids = [
    apple_game_center_leaderboard_set_member.weekly.leaderboard_id,
    apple_game_center_leaderboard_set_member.high_scores.leaderboard_id,
  ]
}
