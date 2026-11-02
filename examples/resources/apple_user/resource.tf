# Invite a developer with access limited to the apps granted below.
resource "apple_user" "dev" {
  email            = "dev@example.com"
  first_name       = "Dev"
  last_name        = "Eloper"
  roles            = ["DEVELOPER"]
  all_apps_visible = false
}

resource "apple_user" "finance" {
  email      = "finance@example.com"
  first_name = "Fin"
  last_name  = "Ance"
  roles      = ["FINANCE"]
}
