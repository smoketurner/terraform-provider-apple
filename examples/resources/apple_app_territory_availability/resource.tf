data "apple_territories" "all" {}

# Sell the app everywhere.
resource "apple_app_territory_availability" "all" {
  for_each  = data.apple_territories.all.ids
  app_id    = apple_app_availability.example.app_id
  territory = each.key
  available = true
}
