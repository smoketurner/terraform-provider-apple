resource "apple_app_tag" "puzzle" {
  app_id               = data.apple_app.example.id
  name                 = "Puzzle"
  visible_in_app_store = false
}
