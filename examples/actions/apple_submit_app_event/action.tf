action "apple_submit_app_event" "summer" {
  config {
    app_id       = data.apple_app.example.id
    app_event_id = apple_app_event.summer.id
  }
}
