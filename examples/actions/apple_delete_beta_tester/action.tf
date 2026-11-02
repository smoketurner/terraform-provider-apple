# Run with: terraform apply -invoke=action.apple_delete_beta_tester.former
data "apple_beta_tester" "former" {
  email = "former@example.com"
}

action "apple_delete_beta_tester" "former" {
  config {
    beta_tester_id = data.apple_beta_tester.former.id
  }
}
