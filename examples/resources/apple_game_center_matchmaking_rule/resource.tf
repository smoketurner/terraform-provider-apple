resource "apple_game_center_matchmaking_rule" "skill" {
  rule_set_id    = apple_game_center_matchmaking_rule_set.ranked.id
  reference_name = "skill"
  type           = "DISTANCE"
  description    = "Match players of similar skill."
  expression     = "abs(requests[0].properties.skill - requests[1].properties.skill)"
  weight         = 0.5
}
