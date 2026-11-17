resource "apple_game_center_challenge_image" "en_US" {
  challenge_id = apple_game_center_challenge.example.id
  locale       = apple_game_center_challenge_localization.en_US.locale
  file         = "${path.module}/challenge-en.png"
}
