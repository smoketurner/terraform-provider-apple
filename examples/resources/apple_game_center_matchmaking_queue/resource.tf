resource "apple_game_center_matchmaking_queue" "ranked" {
  reference_name = "ranked"
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
}
