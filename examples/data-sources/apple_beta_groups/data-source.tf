data "apple_beta_groups" "external" {
  app_id   = data.apple_app.example.id
  internal = false
}
