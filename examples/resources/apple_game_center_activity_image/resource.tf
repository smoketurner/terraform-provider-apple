resource "apple_game_center_activity_image" "en_US" {
  activity_id = apple_game_center_activity.example.id
  locale      = apple_game_center_activity_localization.en_US.locale
  file        = "${path.module}/activity-en.png"
}
