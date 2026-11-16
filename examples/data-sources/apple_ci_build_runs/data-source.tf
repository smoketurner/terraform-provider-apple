data "apple_ci_build_runs" "ci" {
  workflow_id = apple_ci_workflow.ci.id
  limit       = 5
}

output "latest_build_status" {
  value = try(data.apple_ci_build_runs.ci.build_runs[0].completion_status, null)
}
