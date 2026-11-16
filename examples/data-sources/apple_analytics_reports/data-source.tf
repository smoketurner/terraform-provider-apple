data "apple_analytics_reports" "sessions" {
  request_id = apple_analytics_report_request.ongoing.id
  names      = ["App Sessions Standard"]
}
