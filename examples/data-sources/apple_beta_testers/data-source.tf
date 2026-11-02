data "apple_beta_testers" "public" {
  beta_group_id = apple_beta_group.public.id
}
