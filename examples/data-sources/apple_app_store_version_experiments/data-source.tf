data "apple_app_store_version_experiments" "running" {
  app_id = data.apple_app.example.id
  states = ["APPROVED"]
}
