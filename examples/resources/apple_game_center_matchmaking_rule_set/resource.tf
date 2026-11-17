resource "apple_game_center_matchmaking_rule_set" "ranked" {
  reference_name        = "Ranked"
  rule_language_version = 1
  min_players           = 2
  max_players           = 8
}
