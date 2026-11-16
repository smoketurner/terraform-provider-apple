data "apple_retention_messages" "app" {
  app_id = data.apple_app.example.id
}
