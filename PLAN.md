# terraform-provider-apple plan

## Context

There is no complete Terraform provider for Apple. The App Store Connect API is a REST API with an official OpenAPI spec (v4.5.1: 1,303 operations, all with operationIds, 1,473 schemas, 147 creatable resource types). The strongest existing provider, `AhmedOsman00/apple` (v0.4.1, one author, 0 stars), covers about 34 resource types with a hand-written client. Nobody covers Xcode Cloud, Game Center, App Clips, custom product pages, webhooks, offer codes, and so on.

The goal is to make `smoketurner/apple` the most complete Apple provider, built the same way as `smoketurner/discord` and tracked against the spec. v0.1.0 is code signing: the use case that started this.

Decisions already made:
- **Client:** generated from Apple's spec, unlike the hand-written Discord client.
- **Tests:** an in-memory fake API only. There is no live Apple account.
- **v0.1.0 scope:** code signing.

The repo `/Users/jplock/envs/git/terraform-provider-apple` currently has only `.github/FUNDING.yml`; its remote is `smoketurner/terraform-provider-apple`.


---

## 1. Repository scaffold (copy from terraform-provider-discord)

Reuse these from `/Users/jplock/envs/git/terraform-provider-discord`, renaming `discord` to `apple`:
- `main.go`, `go.mod` (same Go and terraform-plugin-framework versions; recheck the latest at the time).
- `GNUmakefile` (targets `fmt lint build test generate api-coverage`; drop `testacc` and `sweep` because there are no live tests).
- `.golangci.yml` (same linters, plus a depguard ban on terraform-plugin-sdk/v2).
- `.pre-commit-config.yaml` for prek (gofmt, golangci-lint, go mod tidy, terraform fmt, actionlint, zizmor).
- `.goreleaser.yml`, `release-please-config.json`, `.release-please-manifest.json`, `terraform-registry-manifest.json`.
- `.github/workflows/ci.yml`, `release.yml`, `api-spec-watch.yml`, Dependabot config.
- `tools/tools.go` (tfplugindocs), `templates/index.md.tmpl`, `examples/`, `CONTRIBUTING.md`, `LICENSE` (MPL-2.0).
- `.claude/` project settings, if they carry over.

Leave out `acceptance.yml` and the live-test scaffolding.

## 2. Spec handling (`internal/apispec`)

Apple publishes the spec only as an unversioned ZIP: `https://developer.apple.com/sample-code/app-store-connect/app-store-connect-openapi-specification.zip`. Unlike Discord's spec, it can't be re-downloaded at a pinned commit, so the spec must be **vendored**:
- `api/openapi.oas.json`: the pinned spec (7.5 MB). Its `info.version` and SHA-256 are recorded in `internal/apispec/pin.go`.
- `apispec fetch` downloads and unzips the latest spec. `apispec watch` diffs the vendored spec against the latest and reports new operations, plus new request fields on operations already covered.
- `coverage.yaml` lists every operationId with a status: `covered` (and `by:`), `planned`, or `out_of_scope` (and `reason:`). It uses the same format and test as the Discord provider (`internal/apispec/coverage_test.go`). At start, everything is `planned` except obvious read-only analytics and sales reports, which stay `planned` as data sources.
- The weekly `api-spec-watch.yml` opens one `api-spec` issue per finding, as in the Discord provider.

## 3. Generated client (`internal/asc`)

- **Generator:** use `oapi-codegen` v2 (look up the current version; pin it via a `tools/` go.mod `tool` directive). It generates types and a `ClientWithResponses` from the vendored spec.
- **Filter:** generate only the operationIds marked `covered` or in progress in `coverage.yaml`, using oapi-codegen's `include-operation-ids` output option. This avoids compiling about 1,300 operations of unused code. A `make generate` step turns the coverage manifest into the generator config, and CI fails if the generated code is stale.
- **Spike first:** generate the code signing operations and confirm the JSON:API `oneOf` unions (`included`, relationships) compile and decode properly. If oapi-codegen can't handle them, switch to `ogen` before building anything on top.
- **Hand-written pieces in `internal/asc`:**
  - **Authentication:** ES256 JWT with issuer ID, key ID and `.p8` key, audience `appstoreconnect-v1`, re-signed before expiry. Use stdlib `crypto/ecdsa` with no JWT dependency. Check the claims and maximum lifetime against Apple's "Generating Tokens for API Requests" before writing.
  - **HTTP handling:** pagination (follow `links.next`), retry with backoff on 429/5xx, and decoding Apple's `errors[]` into Terraform diagnostics. Confirm rate-limit headers against the docs.

## 4. Fake API (`internal/asc/asctest`)

An in-memory `httptest` server modeled on `internal/discord/discordtest/server.go`. The provider's acceptance tests (`resource.Test` with `TF_ACC` forced) run against it in `make test`. It must encode Apple's documented behaviors, because no live tests will catch the gaps:
- Certificates are immutable apart from `activated`. Delete revokes the certificate. Each certificate type has a per-type cap, which returns an error.
- Profiles can't be updated. A profile becomes `INVALID` when one of its certificates is revoked.
- Devices can't be deleted, only set to `DISABLED`.
- Bundle ID capabilities can be created, patched and deleted.

Where possible, use generated server types (oapi-codegen `std-http` server) so the fake stays in sync with the spec. Cite the Apple doc page for each behavior in a comment.

## 5. v0.1.0 resources: code signing

The schemas below are based on the pinned spec's create and update request bodies.

| Resource | Spec ops | Notes |
|---|---|---|
| `apple_bundle_id` | bundleIds get/post/patch/delete | `name` can be updated in place; `identifier` and `platform` force replacement. |
| `apple_bundle_id_capability` | bundleIdCapabilities post/patch/delete | `settings` can be patched. Import ID is `<bundle_id>/<capability_type>`. |
| `apple_certificate` | certificates get/post/patch/delete | Inputs: `certificate_type`, `csr_content` (forces replacement), optional `merchant_id` or `pass_type_id`, and `activated` (updatable). Outputs: `certificate_content`, `serial_number`, `expiration_date`. Also `early_renewal_hours` and `ready_for_renewal`. Docs recommend `create_before_destroy`, with an example using the `tls` provider to generate the key and CSR. |
| `apple_device` | devices get/post/patch | `name` and `status` can be updated. Destroy sets `DISABLED` and warns that the device slot isn't freed until the membership year renews. |
| `apple_profile` | profiles get/post/delete | Every input forces replacement. Outputs: `profile_content` and `expiration_date`. If Read finds `profileState=INVALID`, the profile is removed from state so the next plan recreates it. |
| `apple_merchant_id`, `apple_pass_type_id` | get/post/patch/delete | `name` can be updated. |

Data sources:
- **Singular:** look up by ID or natural key. For example, `apple_certificate` by serial number and `apple_device` by UDID.
- **Plural:** filters that mirror the spec's `filter[...]` parameters.
- **Read-only:** `apple_app` and `apple_apps`, because the API can't create apps.

Imports should accept Apple's ID or the natural key (bundle identifier, serial number, UDID, profile name). Mark these sensitive: the provider's `private_key`, `certificate_content`, and `profile_content`.

Provider configuration: `issuer_id`, `key_id`, `private_key`, with environment variables `APP_STORE_CONNECT_ISSUER_ID`, `APP_STORE_CONNECT_KEY_ID`, `APP_STORE_CONNECT_PRIVATE_KEY`. Check whether to support individual API keys, which have no issuer ID, against the token docs.

## 6. Roadmap after v0.1.0

Open a pinned roadmap issue with one issue per group, in this order:
1. TestFlight: beta groups, testers, localizations, review details.
2. Users and invitations.
3. App metadata: app info, versions, localizations, age rating, review details, phased releases, encryption declarations.
4. Pricing and availability.
5. In-app purchases and subscriptions, including all offer types and win-back offers.
6. Xcode Cloud workflows.
7. Webhooks.
8. Custom product pages and experiments.
9. App events.
10. App Clips.
11. Game Center.
12. Screenshots and previews (multi-step upload).
13. Alternative distribution.
14. Analytics report requests, plus actions and list resources where they fit.

Each issue cites the operationIds it moves to `covered`.

## 7. Steps that need you

- Use a new GPG signing key for releases (the Discord key's private half was discarded). Add it to a `release` GitHub environment and to the `smoketurner` HCP Terraform namespace.
- Set up a branch ruleset with a merge queue and the required CI checks.
- Publish the provider from the namespace after the first release.

## Critical files (new repo)

`PLAN.md`, `api/openapi.oas.json`, `coverage.yaml`, `internal/apispec/*`, `internal/asc/{auth,client,gen}.go`, `internal/asc/asctest/server.go`, `internal/provider/{provider,resource_*,data_source_*}.go`, `.github/workflows/*`.

## Verification

- `make generate`, then `git diff --exit-code`: the generated client and docs are current.
- `make lint`: zero warnings.
- `make test`: unit tests plus acceptance tests against the fake API. These cover create/read/update/import/destroy for every resource; certificate renewal through `early_renewal_hours`; profile invalidation after a certificate is revoked; disabling a device on destroy; and API error decoding, including 409 cap errors and 429 retries.
- `make api-coverage`: every operationId in the vendored spec has a status.
- JWT test: sign with a generated P-256 key and verify the claims and header against Apple's documented format.
- prek and actionlint pass on all workflows.
