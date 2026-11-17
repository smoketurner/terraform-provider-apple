resource "apple_game_center_leaderboard_set" "seasons" {
  game_center_detail_id = apple_game_center_detail.example.id
  reference_name        = "Seasons"
  vendor_identifier     = "com.example.seasons"
}
