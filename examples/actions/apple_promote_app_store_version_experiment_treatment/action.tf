action "apple_promote_app_store_version_experiment_treatment" "orange" {
  config {
    version_id   = apple_app_store_version.example.id
    treatment_id = apple_app_store_version_experiment_treatment.orange.id
  }
}
