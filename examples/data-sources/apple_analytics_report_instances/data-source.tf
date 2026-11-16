data "apple_analytics_report_instances" "daily" {
  report_id     = data.apple_analytics_reports.sessions.reports[0].id
  granularities = ["DAILY"]
}
