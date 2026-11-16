resource "apple_marketplace_search_detail" "catalog" {
  app_id      = data.apple_app.marketplace.id
  catalog_url = "https://example.com/catalog"
}
