resource "apple_game_center_achievement_order" "example" {
  game_center_detail_id = apple_game_center_detail.example.id
  achievement_ids       = [apple_game_center_achievement.first_win.id, apple_game_center_achievement.streak.id]
}
