resource "apple_beta_app_review_detail" "example" {
  app_id                = data.apple_app.example.id
  contact_email         = "review@example.com"
  contact_first_name    = "Rev"
  contact_last_name     = "Iewer"
  contact_phone         = "+1 555 0100"
  demo_account_required = true
  demo_account_name     = "demo@example.com"
  demo_account_password = var.demo_password
  notes                 = "Sign in with the demo account."
}

variable "demo_password" {
  type      = string
  sensitive = true
}
