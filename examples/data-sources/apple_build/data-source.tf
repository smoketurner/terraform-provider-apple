# The newest build of version 1.2.0.
data "apple_build" "latest" {
  app_id              = data.apple_app.example.id
  pre_release_version = "1.2.0"
  latest              = true
}
