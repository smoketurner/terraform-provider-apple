data "apple_analytics_report_segments" "latest" {
  instance_id = data.apple_analytics_report_instances.daily.instances[0].id
}

output "report_urls" {
  value     = data.apple_analytics_report_segments.latest.segments[*].url
  sensitive = true
}
