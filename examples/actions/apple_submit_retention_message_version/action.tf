action "apple_submit_retention_message_version" "first" {
  config {
    app_id                       = data.apple_app.example.id
    retention_message_version_id = apple_retention_message_version.first.id
  }
}
