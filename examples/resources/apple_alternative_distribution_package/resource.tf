resource "apple_alternative_distribution_package" "example" {
  app_store_version_id = apple_app_store_version.example.id
}
