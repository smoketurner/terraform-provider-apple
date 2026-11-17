resource "apple_game_center_leaderboard_set_member_localization" "en_US" {
  leaderboard_set_id = apple_game_center_leaderboard_set_member.weekly.leaderboard_set_id
  leaderboard_id     = apple_game_center_leaderboard_set_member.weekly.leaderboard_id
  locale             = "en-US"
  name               = "This Week"
}
