resource "apple_app_asset_library_video" "tour" {
  app_id                  = data.apple_app.example.id
  file                    = "${path.module}/previews/tour.mov"
  category                = "APP_SCREENSHOTS_AND_PREVIEWS"
  preview_frame_time_code = "00:00:05:00"
}
