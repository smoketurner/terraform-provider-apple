action "apple_submit_game_center_leaderboard" "high_scores" {
  config {
    app_id  = data.apple_app.example.id
    item_id = apple_game_center_leaderboard.high_scores.id
  }
}
