data "apple_build" "latest" {
  app_id              = data.apple_app.example.id
  pre_release_version = "1.2.0"
  latest              = true
}

resource "apple_beta_build_localization" "en" {
  build_id  = data.apple_build.latest.id
  locale    = "en-US"
  whats_new = "Try the new checkout flow."
}
