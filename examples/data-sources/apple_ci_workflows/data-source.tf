data "apple_ci_workflows" "product" {
  product_id = data.apple_ci_products.app.products[0].id
}
