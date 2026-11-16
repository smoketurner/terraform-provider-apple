data "apple_ci_products" "app" {
  app_id = data.apple_app.example.id
}

data "apple_ci_xcode_versions" "all" {}

locals {
  product = data.apple_ci_products.app.products[0]
  xcode   = one([for v in data.apple_ci_xcode_versions.all.xcode_versions : v if v.version == "26.0"])
}

resource "apple_ci_workflow" "ci" {
  product_id          = local.product.id
  repository_id       = local.product.primary_repository_id
  xcode_version_id    = local.xcode.id
  mac_os_version_id   = local.xcode.mac_os_version_ids[0]
  name                = "CI"
  description         = "Builds and tests changes to main and pull requests into it."
  enabled             = true
  clean               = false
  container_file_path = "Example.xcodeproj"

  branch_start_condition = {
    source      = { patterns = [{ pattern = "main" }] }
    auto_cancel = true
  }

  pull_request_start_condition = {
    source      = { is_all_match = true }
    destination = { patterns = [{ pattern = "main" }] }
    auto_cancel = true
  }

  scheduled_start_condition = {
    source = { patterns = [{ pattern = "main" }] }
    schedule = {
      frequency = "WEEKLY"
      days      = ["MONDAY"]
      hour      = 6
      minute    = 0
      timezone  = "America/Los_Angeles"
    }
  }

  actions = [
    {
      name                = "Build - iOS"
      action_type         = "BUILD"
      scheme              = "Example"
      platform            = "IOS"
      destination         = "ANY_IOS_DEVICE"
      is_required_to_pass = true
    },
    {
      name                = "Test - iOS"
      action_type         = "TEST"
      scheme              = "Example"
      platform            = "IOS"
      destination         = "ANY_IOS_SIMULATOR"
      is_required_to_pass = true
      test_configuration = {
        kind = "USE_SCHEME_SETTINGS"
      }
    },
  ]
}
