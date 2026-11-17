resource "apple_game_center_matchmaking_team" "red" {
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
  reference_name = "red"
  min_players    = 1
  max_players    = 4
}
