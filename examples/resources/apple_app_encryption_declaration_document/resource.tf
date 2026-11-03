resource "apple_app_encryption_declaration_document" "ccats" {
  declaration_id = apple_app_encryption_declaration.example.id
  file           = "${path.module}/compliance/ccats.pdf"
}
