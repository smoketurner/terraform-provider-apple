data "apple_nominations" "drafts" {
  states  = ["DRAFT"]
  app_ids = [data.apple_app.example.id]
}
