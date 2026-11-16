data "apple_app_events" "published" {
  app_id = data.apple_app.example.id
  states = ["PUBLISHED"]
}
