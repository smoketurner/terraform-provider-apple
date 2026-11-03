action "apple_ping_webhook" "builds" {
  config {
    webhook_id = apple_webhook.builds.id
  }
}
