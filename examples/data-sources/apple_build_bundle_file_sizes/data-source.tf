data "apple_build_bundle_file_sizes" "app" {
  build_bundle_id = data.apple_build_bundles.latest.build_bundles[0].id
}

# Fail the plan when the universal download grows past 200 MB.
check "download_size" {
  assert {
    condition = alltrue([
      for size in data.apple_build_bundle_file_sizes.app.file_sizes : size.download_bytes < 200 * 1024 * 1024
      if size.device_model == "Universal"
    ])
    error_message = "The app's universal download is larger than 200 MB."
  }
}
