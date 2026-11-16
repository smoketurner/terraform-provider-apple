resource "apple_app_clip_header_image" "en_US" {
  localization_id = apple_app_clip_default_experience_localization.en_US.id
  file            = "${path.module}/app-clip-card.png"
}
