data "apple_background_asset_versions" "complete" {
  background_asset_id = apple_background_asset.tutorial.id
  states              = ["COMPLETE"]
}
