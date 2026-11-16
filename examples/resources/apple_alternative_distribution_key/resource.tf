# The key for every alternative distribution app of the account.
resource "apple_alternative_distribution_key" "account" {
  public_key = file("${path.module}/alternative-distribution-public-key.pem")
}

# A key for one app.
resource "apple_alternative_distribution_key" "app" {
  app_id     = data.apple_app.example.id
  public_key = file("${path.module}/example-app-public-key.pem")
}
