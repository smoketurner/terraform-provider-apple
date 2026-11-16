action "apple_submit_game_center_achievement" "first_win" {
  config {
    app_id  = data.apple_app.example.id
    item_id = apple_game_center_achievement.first_win.id
  }
}
