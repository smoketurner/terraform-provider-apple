resource "apple_app_store_version_experiment_treatment" "orange" {
  experiment_id = apple_app_store_version_experiment.icons.id
  name          = "Orange icon"
  app_icon_name = "OrangeIcon"
}
