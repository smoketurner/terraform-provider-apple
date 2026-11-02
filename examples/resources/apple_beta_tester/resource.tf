resource "apple_beta_tester" "ann" {
  email          = "ann@example.com"
  first_name     = "Ann"
  last_name      = "Tester"
  beta_group_ids = [apple_beta_group.public.id]
}
