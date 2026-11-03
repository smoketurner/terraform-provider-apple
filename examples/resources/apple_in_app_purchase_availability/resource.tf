data "apple_territories" "all" {}

resource "apple_in_app_purchase_availability" "coins" {
  in_app_purchase_id           = apple_in_app_purchase.coins.id
  available_in_new_territories = true
  territories                  = data.apple_territories.all.ids
}
