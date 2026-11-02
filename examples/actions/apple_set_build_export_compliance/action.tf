# Distribute each new build: answer export compliance, add it to the groups,
# and submit it for beta review. Changing build_id runs the actions again.
resource "terraform_data" "release" {
  input = data.apple_build.latest.id
  lifecycle {
    action_trigger {
      events = [after_create, after_update]
      actions = [
        action.apple_set_build_export_compliance.latest,
        action.apple_add_build_to_beta_groups.latest,
        action.apple_submit_beta_review.latest,
      ]
    }
  }
}

action "apple_set_build_export_compliance" "latest" {
  config {
    build_id                   = data.apple_build.latest.id
    uses_non_exempt_encryption = false
  }
}
