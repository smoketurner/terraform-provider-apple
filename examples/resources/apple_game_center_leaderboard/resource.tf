# A classic leaderboard.
resource "apple_game_center_leaderboard" "high_scores" {
  game_center_detail_id = apple_game_center_detail.example.id
  reference_name        = "High scores"
  vendor_identifier     = "com.example.high_scores"
  default_formatter     = "INTEGER"
  submission_type       = "BEST_SCORE"
  score_sort_type       = "DESC"
}

# A recurring leaderboard that starts over every week.
resource "apple_game_center_leaderboard" "weekly" {
  game_center_detail_id = apple_game_center_detail.example.id
  reference_name        = "Weekly scores"
  vendor_identifier     = "com.example.weekly_scores"
  default_formatter     = "INTEGER"
  submission_type       = "BEST_SCORE"
  score_sort_type       = "DESC"
  recurrence_start_date = "2026-12-07T08:00:00Z"
  recurrence_duration   = "P7D"
  recurrence_rule       = "FREQ=WEEKLY;INTERVAL=1"
}
