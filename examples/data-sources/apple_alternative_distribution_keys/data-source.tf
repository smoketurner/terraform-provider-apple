data "apple_alternative_distribution_keys" "all" {}

data "apple_alternative_distribution_keys" "app_specific" {
  app_specific = true
}
