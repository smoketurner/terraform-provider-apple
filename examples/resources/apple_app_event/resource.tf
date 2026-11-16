resource "apple_app_event" "summer" {
  app_id               = data.apple_app.example.id
  reference_name       = "Summer Cup"
  badge                = "COMPETITION"
  deep_link            = "https://example.com/events/summer"
  purchase_requirement = "NO_COST_ASSOCIATED"
  primary_locale       = "en-US"
  priority             = "HIGH"
  purpose              = "ATTRACT_NEW_USERS"

  territory_schedules = [{
    territories   = ["USA", "CAN"]
    publish_start = "2027-06-01T16:00:00Z"
    event_start   = "2027-06-15T16:00:00Z"
    event_end     = "2027-06-30T16:00:00Z"
  }]
}
