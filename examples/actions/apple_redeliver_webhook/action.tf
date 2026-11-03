action "apple_redeliver_webhook" "last_failure" {
  config {
    delivery_id = data.apple_webhook_deliveries.failed.deliveries[0].id
  }
}
