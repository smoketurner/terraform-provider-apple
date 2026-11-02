data "apple_pre_release_versions" "ios" {
  app_id    = data.apple_app.example.id
  platforms = ["IOS"]
}
