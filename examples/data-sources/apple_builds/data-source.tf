data "apple_builds" "testable" {
  app_id            = data.apple_app.example.id
  processing_states = ["VALID"]
  expired           = false
}
