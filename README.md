# Terraform Provider for Apple

Manage App Store Connect as code with Terraform, through the
[App Store Connect API](https://developer.apple.com/documentation/appstoreconnectapi).

Built on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework), with an API
client generated from Apple's OpenAPI spec. It will be published to the Terraform Registry as `smoketurner/apple`.

**Status:** in development, working toward v0.1.0, which covers code signing. The
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

See the [documentation](docs/index.md) for API key setup and [CONTRIBUTING.md](CONTRIBUTING.md) for development.
