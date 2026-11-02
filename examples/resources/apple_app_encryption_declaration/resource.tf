resource "apple_app_encryption_declaration" "example" {
  app_id                            = data.apple_app.example.id
  app_description                   = "The app uses HTTPS only."
  contains_proprietary_cryptography = false
  contains_third_party_cryptography = false
  available_on_french_store         = true
}
