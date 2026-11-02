action "apple_remove_build_testers" "ann" {
  config {
    build_id        = data.apple_build.latest.id
    beta_tester_ids = [apple_beta_group_tester.ann.beta_tester_id]
  }
}
