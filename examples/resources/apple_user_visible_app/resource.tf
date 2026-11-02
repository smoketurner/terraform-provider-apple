# Applies once dev@example.com has accepted their invitation.
resource "apple_user_visible_app" "dev_example" {
  user_email = apple_user.dev.email
  app_id     = data.apple_app.example.id
}
