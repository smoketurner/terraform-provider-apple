data "apple_scm_pull_requests" "all" {
  repository_id = data.apple_ci_products.app.products[0].primary_repository_id
}

output "open_pull_requests" {
  value = [for pr in data.apple_scm_pull_requests.all.pull_requests : pr.title if !pr.is_closed]
}
