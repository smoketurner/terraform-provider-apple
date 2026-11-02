action "apple_remove_build_from_beta_groups" "public" {
  config {
    build_id       = data.apple_build.latest.id
    beta_group_ids = [apple_beta_group.public.id]
  }
}
