data "apple_app_asset_library_reference_data" "all" {}

output "iphone_groups" {
  value = [for g in data.apple_app_asset_library_reference_data.all.placement_groups : g.id if g.platform == "IPHONE_APP_STORE"]
}
