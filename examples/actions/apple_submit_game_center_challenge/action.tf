action "apple_submit_game_center_challenge" "example" {
  config {
    app_id  = data.apple_app.example.id
    item_id = apple_game_center_challenge.example.id
  }
}
