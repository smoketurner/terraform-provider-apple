data "apple_scm_repositories" "all" {}

data "apple_scm_repositories" "product" {
  product_id = data.apple_ci_products.app.products[0].id
}
