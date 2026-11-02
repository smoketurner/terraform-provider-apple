resource "apple_age_rating_declaration" "example" {
  app_id                      = data.apple_app.example.id
  violence_cartoon_or_fantasy = "INFREQUENT_OR_MILD"
  gambling                    = false
  user_generated_content      = false
  messaging_and_chat          = false
}
