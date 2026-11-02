action "apple_submit_beta_review" "latest" {
  config {
    build_id = data.apple_build.latest.id
  }
}
