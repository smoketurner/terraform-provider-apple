resource "apple_game_center_achievement_localization" "en_US" {
  achievement_id            = apple_game_center_achievement.first_win.id
  locale                    = "en-US"
  name                      = "First Win"
  before_earned_description = "Win a match."
  after_earned_description  = "You won your first match."
}
