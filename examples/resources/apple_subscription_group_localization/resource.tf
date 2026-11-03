resource "apple_subscription_group_localization" "en_us" {
  subscription_group_id = apple_subscription_group.pro.id
  locale                = "en-US"
  name                  = "Example Pro"
}
