# Terraform Provider for Apple

Manage App Store Connect as code with Terraform, through the
[App Store Connect API](https://developer.apple.com/documentation/appstoreconnectapi).

Built on the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework), with an API
client generated from Apple's OpenAPI spec. It will be published to the Terraform Registry as `smoketurner/apple`.

**Status:** in development. v0.1.0 covers code signing: bundle IDs and their capabilities, certificates, devices,
provisioning profiles, merchant IDs and pass type IDs. The [roadmap](https://github.com/smoketurner/terraform-provider-apple/issues/17)
lists every group of API operations planned after that, and [`coverage.yaml`](coverage.yaml) maps each of the spec's
operations to its status.

See the [documentation](docs/index.md) for API key setup and [CONTRIBUTING.md](CONTRIBUTING.md) for development.
