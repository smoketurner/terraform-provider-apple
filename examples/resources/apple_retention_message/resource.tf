resource "apple_retention_message" "stay" {
  app_id         = data.apple_app.example.id
  reference_name = "Stay with us"
}
