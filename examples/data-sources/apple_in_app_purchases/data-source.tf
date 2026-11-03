data "apple_in_app_purchases" "consumables" {
  app_id = data.apple_app.example.id
  types  = ["CONSUMABLE"]
}
