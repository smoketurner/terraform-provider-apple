action "apple_end_pre_orders" "launch" {
  config {
    app_id      = data.apple_app.example.id
    territories = ["USA", "CAN"]
  }
}
