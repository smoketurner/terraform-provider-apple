data "apple_alternative_distribution_package_version" "current" {
  id = data.apple_alternative_distribution_package_versions.current.versions[0].id
}

output "variant_urls" {
  value     = data.apple_alternative_distribution_package_version.current.variants[*].url
  sensitive = true
}
