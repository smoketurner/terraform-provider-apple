resource "apple_game_center_detail" "example" {
  app_id               = data.apple_app.example.id
  game_center_group_id = apple_game_center_group.example.id
}
