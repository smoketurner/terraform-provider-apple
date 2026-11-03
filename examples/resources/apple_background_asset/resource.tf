resource "apple_background_asset" "tutorial" {
  app_id                = data.apple_app.example.id
  asset_pack_identifier = "Tutorial"
}
