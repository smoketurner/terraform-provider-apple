action "apple_assign_encryption_declaration" "latest" {
  config {
    build_id       = data.apple_build.latest.id
    declaration_id = apple_app_encryption_declaration.example.id
  }
}
