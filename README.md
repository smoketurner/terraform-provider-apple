# Terraform Provider for Apple

Manage App Store Connect as code with Terraform, through the
[App Store Connect API](https://developer.apple.com/documentation/appstoreconnectapi).

Built on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework), with an API
client generated from Apple's OpenAPI spec. It will be published to the Terraform Registry as `smoketurner/apple`.

**Status:** in development. v0.1.0 covers code signing; TestFlight is next. The
[roadmap](https://github.com/smoketurner/terraform-provider-apple/issues/17) lists every group of API operations
planned after that, and [`coverage.yaml`](coverage.yaml) maps each of the spec's operations to its status.

| Kind | Names |
|------|-------|
| Bundle IDs | `apple_bundle_id`, `apple_bundle_id_capability`, `data.apple_bundle_id`, `data.apple_bundle_ids` |
| Certificates | `apple_certificate`, `data.apple_certificate`, `data.apple_certificates` |
| Devices | `apple_device`, `data.apple_device`, `data.apple_devices` |
| Provisioning profiles | `apple_profile`, `data.apple_profile`, `data.apple_profiles` |
| Merchant and pass type IDs | `apple_merchant_id`, `apple_pass_type_id`, `data.apple_merchant_id`, `data.apple_merchant_ids`, `data.apple_pass_type_id`, `data.apple_pass_type_ids` |
| Apps (read-only) | `data.apple_app`, `data.apple_apps` |
| TestFlight | `apple_beta_group`, `apple_beta_group_tester`, `apple_beta_app_localization`, `apple_beta_build_localization`, `apple_beta_app_review_detail`, `apple_beta_license_agreement`, `apple_build_beta_detail`, `apple_beta_recruitment_criteria`, `data.apple_build`, `data.apple_builds`, `data.apple_pre_release_versions`, `data.apple_beta_groups`, `data.apple_beta_tester`, `data.apple_beta_testers`, `data.apple_beta_recruitment_criterion_options` |
| App information | `apple_app_info`, `apple_app_info_localization`, `apple_age_rating_declaration`, `apple_app_settings`, `apple_end_user_license_agreement`, `apple_accessibility_declaration`, `apple_android_to_ios_app_mapping`, `apple_app_tag`, `data.apple_app_categories` |
| App Store versions | `apple_app_store_version`, `apple_app_store_version_localization`, `apple_app_store_review_detail`, `apple_app_store_version_phased_release`, `apple_app_encryption_declaration`, `apple_app_store_version_localization_search_keyword`, `data.apple_app_search_keywords` |
| Screenshots, previews and files | `apple_app_screenshot_set`, `apple_app_preview_set`, `apple_app_store_review_attachment`, `apple_app_encryption_declaration_document`, `apple_app_store_version_routing_app_coverage` |
| In-app purchases | `apple_in_app_purchase`, `apple_in_app_purchase_localization`, `apple_in_app_purchase_image`, `apple_in_app_purchase_review_screenshot`, `apple_in_app_purchase_price_schedule`, `apple_in_app_purchase_availability`, `apple_sandbox_tester`, `data.apple_in_app_purchases`, `data.apple_in_app_purchase_price_points`, `data.apple_in_app_purchase_price_point_equalizations`, `data.apple_sandbox_testers`, `apple_submit_in_app_purchase` and `apple_clear_sandbox_purchase_history` actions |
| Subscriptions | `apple_subscription_group`, `apple_subscription_group_localization`, `apple_subscription`, `apple_subscription_localization`, `apple_subscription_image`, `apple_subscription_review_screenshot`, `apple_subscription_price`, `apple_subscription_plan_availability`, `apple_subscription_grace_period`, `data.apple_subscription_groups`, `data.apple_subscriptions`, `data.apple_subscription_price_points`, `data.apple_subscription_price_point_equalizations`, `apple_submit_subscription` action |
| Offers and promotions | `apple_subscription_introductory_offer`, `apple_subscription_promotional_offer`, `apple_win_back_offer`, `apple_promoted_purchase`, `apple_promoted_purchase_order`, `apple_subscription_offer_code`, `apple_in_app_purchase_offer_code`, `apple_subscription_offer_code_custom_code`, `apple_subscription_offer_code_one_time_use_codes`, `apple_in_app_purchase_offer_code_custom_code`, `apple_in_app_purchase_offer_code_one_time_use_codes`, `data.apple_subscription_offer_code_one_time_use_code_values`, `data.apple_in_app_purchase_offer_code_one_time_use_code_values` |
| App Asset Library | `apple_app_asset_library_image`, `apple_app_asset_library_video`, `apple_app_asset_library_placement`, `apple_app_asset_library_placement_order`, `data.apple_app_asset_library`, `data.apple_app_asset_library_placements`, `data.apple_app_asset_library_reference_data` |
| Background assets | `apple_background_asset`, `apple_background_asset_version`, `data.apple_background_assets`, `data.apple_background_asset_versions` |
| Featuring nominations | `apple_nomination`, `data.apple_nominations` |
| Webhooks | `apple_webhook`, `data.apple_webhooks`, `data.apple_webhook_deliveries`, `apple_ping_webhook` and `apple_redeliver_webhook` actions (Terraform 1.14+) |
| Pricing and availability | `apple_app_price_schedule`, `apple_app_availability`, `apple_app_territory_availability`, `data.apple_territories`, `data.apple_app_price_points`, `data.apple_app_price_point_equalizations` |
| Users | `apple_user`, `apple_user_visible_app`, `data.apple_user`, `data.apple_users`, `data.apple_user_invitations` |
| TestFlight actions (Terraform 1.14+) | `apple_add_build_to_beta_groups`, `apple_remove_build_from_beta_groups`, `apple_add_build_testers`, `apple_remove_build_testers`, `apple_set_build_export_compliance`, `apple_submit_beta_review`, `apple_notify_beta_testers`, `apple_invite_beta_tester`, `apple_expire_build`, `apple_remove_beta_testers_from_app`, `apple_delete_beta_tester` |
| App Review and release actions (Terraform 1.14+) | `apple_submit_for_review`, `apple_cancel_review_submission`, `apple_release_version`, `apple_assign_encryption_declaration`, `apple_end_pre_orders` |

See the [documentation](docs/index.md) for API key setup and [CONTRIBUTING.md](CONTRIBUTING.md) for development.
