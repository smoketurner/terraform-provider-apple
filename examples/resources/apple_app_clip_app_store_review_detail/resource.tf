resource "apple_app_clip_app_store_review_detail" "card" {
  default_experience_id = apple_app_clip_default_experience.card.id
  invocation_urls       = ["https://example.com/order"]
}
