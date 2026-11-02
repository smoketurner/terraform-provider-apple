action "apple_add_build_to_beta_groups" "latest" {
  config {
    build_id       = data.apple_build.latest.id
    beta_group_ids = [apple_beta_group.team.id, apple_beta_group.public.id]
  }
}
