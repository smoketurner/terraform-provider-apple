action "apple_remove_beta_testers_from_app" "ann" {
  config {
    app_id          = data.apple_app.example.id
    beta_tester_ids = [apple_beta_group_tester.ann.beta_tester_id]
  }
}
