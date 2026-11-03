resource "apple_app_store_version_routing_app_coverage" "regions" {
  version_id = apple_app_store_version.ios.id
  file       = "${path.module}/routing/coverage.geojson"
}
