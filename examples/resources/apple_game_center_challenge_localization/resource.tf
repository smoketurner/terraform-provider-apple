resource "apple_game_center_challenge_localization" "en_US" {
  challenge_id = apple_game_center_challenge.example.id
  locale       = "en-US"
  name         = "Weekly Duel"
  description  = "Beat your friends this week."
}
