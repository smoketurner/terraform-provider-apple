resource "apple_background_asset_version" "tutorial" {
  background_asset_id = apple_background_asset.tutorial.id
  file                = "${path.module}/assets/Tutorial.aar"
  manifest_file       = "${path.module}/assets/Manifest.json"
}
