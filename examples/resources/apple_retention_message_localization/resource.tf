resource "apple_retention_message_localization" "en_US" {
  retention_message_version_id = apple_retention_message_version.first.id
  locale                       = "en-US"
  header                       = "Your cortado is waiting"
  body                         = "Stay subscribed and keep your daily cortado coming."
}
