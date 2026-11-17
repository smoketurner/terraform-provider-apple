data "apple_game_center_leaderboards" "app" {
  game_center_detail_id = data.apple_game_center_detail.example.id
  archived              = false
}
