resource "apple_app_store_review_detail" "ios" {
  version_id            = apple_app_store_version.ios.id
  contact_first_name    = "Ann"
  contact_last_name     = "Example"
  contact_phone         = "+1 555 555 0100"
  contact_email         = "review@example.com"
  demo_account_required = true
  demo_account_name     = "reviewer@example.com"
  demo_account_password = var.review_password
  notes                 = "Sign in with the demo account to see the full app."
}
