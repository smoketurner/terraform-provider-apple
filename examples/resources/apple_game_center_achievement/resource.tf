resource "apple_game_center_achievement" "first_win" {
  game_center_detail_id = apple_game_center_detail.example.id
  reference_name        = "First win"
  vendor_identifier     = "com.example.first_win"
  points                = 10
  show_before_earned    = true
  repeatable            = false
}
