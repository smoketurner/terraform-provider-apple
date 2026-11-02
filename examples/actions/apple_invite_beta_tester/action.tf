action "apple_invite_beta_tester" "ann" {
  config {
    app_id         = data.apple_app.example.id
    beta_tester_id = apple_beta_group_tester.ann.beta_tester_id
  }
}
