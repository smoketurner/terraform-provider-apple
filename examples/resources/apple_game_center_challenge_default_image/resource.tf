resource "apple_game_center_challenge_default_image" "example" {
  challenge_id = apple_game_center_challenge.example.id
  file         = "${path.module}/challenge.png"
}
