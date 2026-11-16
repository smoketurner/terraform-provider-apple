resource "apple_app_store_version_experiment" "icons" {
  app_id             = data.apple_app.example.id
  platform           = "IOS"
  name               = "Spring icon color test"
  traffic_proportion = 40
}
