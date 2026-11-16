resource "apple_game_center_achievement_image" "en_US" {
  achievement_id = apple_game_center_achievement.first_win.id
  locale         = apple_game_center_achievement_localization.en_US.locale
  file           = "${path.module}/first-win.png"
}
