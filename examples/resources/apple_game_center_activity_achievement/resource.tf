resource "apple_game_center_activity_achievement" "example" {
  activity_id    = apple_game_center_activity.example.id
  achievement_id = apple_game_center_achievement.example.id
}
