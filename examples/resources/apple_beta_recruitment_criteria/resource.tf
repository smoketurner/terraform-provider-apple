resource "apple_beta_recruitment_criteria" "public" {
  beta_group_id = apple_beta_group.public.id
  filters = [
    { device_family = "IPHONE", minimum_os_version = "18.0" },
    { device_family = "IPAD", minimum_os_version = "18.0" },
  ]
}
