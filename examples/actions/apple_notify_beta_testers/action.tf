# Run with: terraform apply -invoke=action.apple_notify_beta_testers.latest
action "apple_notify_beta_testers" "latest" {
  config {
    build_id = data.apple_build.latest.id
  }
}
