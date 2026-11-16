data "apple_scm_git_references" "branches" {
  repository_id = data.apple_ci_products.app.products[0].primary_repository_id
  kinds         = ["BRANCH"]
}

locals {
  main_branch_id = one([for r in data.apple_scm_git_references.branches.git_references : r.id if r.name == "main"])
}
