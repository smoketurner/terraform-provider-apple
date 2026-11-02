resource "apple_build_beta_detail" "latest" {
  build_id            = data.apple_build.latest.id
  auto_notify_enabled = true
}
