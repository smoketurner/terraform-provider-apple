resource "apple_game_center_activity_localization" "en_US" {
  activity_id = apple_game_center_activity.example.id
  locale      = "en-US"
  name        = "Daily Puzzle"
  description = "A new puzzle every day."
}
