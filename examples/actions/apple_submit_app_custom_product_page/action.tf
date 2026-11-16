action "apple_submit_app_custom_product_page" "summer" {
  config {
    app_id                 = data.apple_app.example.id
    custom_product_page_id = apple_app_custom_product_page.summer.id
  }
}
