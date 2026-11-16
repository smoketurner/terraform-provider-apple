resource "apple_analytics_report_request" "ongoing" {
  app_id      = data.apple_app.example.id
  access_type = "ONGOING"
}
