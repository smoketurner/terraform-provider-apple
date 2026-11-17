resource "apple_game_center_activity" "example" {
  game_center_detail_id = apple_game_center_detail.example.id
  reference_name        = "Daily puzzle"
  vendor_identifier     = "com.example.daily_puzzle"
  play_style            = "ASYNCHRONOUS"
  minimum_players_count = 1
  maximum_players_count = 4
  fallback_url          = "https://example.com/daily"
  properties            = { level = "daily" }
}
