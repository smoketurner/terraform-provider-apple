resource "apple_app_availability" "example" {
  app_id                       = data.apple_app.example.id
  available_in_new_territories = true
}
