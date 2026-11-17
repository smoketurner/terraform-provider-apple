resource "apple_game_center_activity_default_image" "example" {
  activity_id = apple_game_center_activity.example.id
  file        = "${path.module}/activity.png"
}
