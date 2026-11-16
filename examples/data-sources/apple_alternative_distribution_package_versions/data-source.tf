data "apple_alternative_distribution_package_versions" "current" {
  package_id = apple_alternative_distribution_package.example.id
  states     = ["COMPLETED"]
}
