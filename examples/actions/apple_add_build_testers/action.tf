action "apple_add_build_testers" "ann" {
  config {
    build_id        = data.apple_build.latest.id
    beta_tester_ids = [apple_beta_tester.ann.id]
  }
}
