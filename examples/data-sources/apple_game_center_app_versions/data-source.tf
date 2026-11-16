data "apple_game_center_app_versions" "example" {
  game_center_detail_id = data.apple_game_center_detail.example.id
}
