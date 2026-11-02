# Add a tester to a group. The tester is created if no tester has the email.
resource "apple_beta_group_tester" "ann" {
  beta_group_id = apple_beta_group.public.id
  email         = "ann@example.com"
  first_name    = "Ann"
  last_name     = "Tester"
}

# Add a list of testers to a group.
resource "apple_beta_group_tester" "team" {
  for_each      = toset(["bo@example.com", "cy@example.com"])
  beta_group_id = apple_beta_group.team.id
  email         = each.key
}
