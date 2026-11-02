resource "apple_app_info" "example" {
  app_id                  = data.apple_app.example.id
  primary_category        = "GAMES"
  primary_subcategory_one = "GAMES_PUZZLE"
  secondary_category      = "ENTERTAINMENT"
}
