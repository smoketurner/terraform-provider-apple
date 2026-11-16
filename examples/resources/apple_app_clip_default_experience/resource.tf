resource "apple_app_clip_default_experience" "card" {
  app_clip_id                       = data.apple_app_clips.app.app_clips[0].id
  release_with_app_store_version_id = apple_app_store_version.example.id
  action                            = "OPEN"
}
