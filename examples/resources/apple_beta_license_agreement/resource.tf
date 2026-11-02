resource "apple_beta_license_agreement" "example" {
  app_id         = data.apple_app.example.id
  agreement_text = file("${path.module}/beta-license.txt")
}
