data "apple_app" "example" {
  bundle_id = "com.example.app"
}

resource "apple_beta_group" "team" {
  app_id                   = data.apple_app.example.id
  name                     = "Team"
  internal                 = true
  has_access_to_all_builds = true
}

resource "apple_beta_group" "public" {
  app_id                    = data.apple_app.example.id
  name                      = "Public Beta"
  public_link_enabled       = true
  public_link_limit_enabled = true
  public_link_limit         = 1000

  # An app needs an internal group before it can have an external one.
  depends_on = [apple_beta_group.team]
}

output "public_link" {
  value = apple_beta_group.public.public_link
}
