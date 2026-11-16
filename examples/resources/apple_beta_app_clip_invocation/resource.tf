locals {
  app_clip_bundle_id = one([
    for bundle in data.apple_build_bundles.latest.build_bundles : bundle.id if bundle.bundle_type == "APP_CLIP"
  ])
}

resource "apple_beta_app_clip_invocation" "order" {
  build_bundle_id = local.app_clip_bundle_id
  url             = "https://example.com/order"
  localizations = [
    { locale = "en-US", title = "Order ahead" },
    { locale = "fr-FR", title = "Commander" },
  ]
}
