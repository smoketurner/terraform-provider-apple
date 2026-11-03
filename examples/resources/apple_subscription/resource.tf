resource "apple_subscription" "monthly" {
  subscription_group_id = apple_subscription_group.pro.id
  product_id            = "com.example.pro.monthly"
  name                  = "Pro Monthly"
  period                = "ONE_MONTH"
  group_level           = 2
}
