data "apple_webhook_deliveries" "failed" {
  webhook_id = apple_webhook.builds.id
  states     = ["FAILED"]
}
