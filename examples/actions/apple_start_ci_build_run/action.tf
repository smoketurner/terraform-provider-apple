action "apple_start_ci_build_run" "main" {
  config {
    workflow_id      = apple_ci_workflow.ci.id
    git_reference_id = local.main_branch_id
  }
}

# Start a build whenever the workflow changes.
resource "terraform_data" "build_on_change" {
  input = apple_ci_workflow.ci.last_modified_date

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.apple_start_ci_build_run.main]
    }
  }
}
