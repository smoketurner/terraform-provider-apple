resource "apple_app_clip_default_experience_localization" "en_US" {
  default_experience_id = apple_app_clip_default_experience.card.id
  locale                = "en-US"
  subtitle              = "Order ahead and skip the line."
}
