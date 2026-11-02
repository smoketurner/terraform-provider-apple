resource "apple_end_user_license_agreement" "example" {
  app_id         = data.apple_app.example.id
  agreement_text = file("${path.module}/license.txt")
  territory_ids  = ["USA", "CAN", "GBR"]
}
