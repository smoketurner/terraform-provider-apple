action "apple_submit_in_app_purchase" "coins" {
  config {
    app_id             = data.apple_app.example.id
    in_app_purchase_id = apple_in_app_purchase.coins.id
  }
}
