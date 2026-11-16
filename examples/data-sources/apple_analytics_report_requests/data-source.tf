data "apple_analytics_report_requests" "ongoing" {
  app_id       = data.apple_app.example.id
  access_types = ["ONGOING"]
}
