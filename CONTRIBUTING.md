# Contributing

## Setup

1. Install Go (the version in `go.mod`), Terraform 1.8+, [golangci-lint](https://golangci-lint.run) and
   [prek](https://github.com/j178/prek).
2. Run `prek install` to enable the Git hooks.

## Making changes

- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`, `docs:`,
  `chore:`). release-please uses them to choose the next version and write the changelog.
- Check API behavior against the [App Store Connect API documentation](https://developer.apple.com/documentation/appstoreconnectapi),
  not against memory or other client libraries. The spec describes request and response shapes; the documentation
  describes what the API does with them, such as which fields can be changed after creation.
- Add or update acceptance tests in `internal/provider`. Cover drift, meaning changes made outside Terraform, and
  import. If the change relies on Apple behavior the fake API does not model yet, extend the fake and cite the Apple
  documentation page for the behavior in a comment.
- After changing a schema, an example or `coverage.yaml`, run `make generate` and commit the updated files.

## API spec and the generated client

Apple publishes its OpenAPI spec as an unversioned ZIP, so the spec is vendored in `api/openapi.oas.json`, and its
version and checksum are pinned in `internal/apispec/pin.go`.

`coverage.yaml` maps every operation in the spec to a status:

- `covered`: the provider calls it, for the resources and data sources listed under `by`.
- `in_progress`: it is being built under the given roadmap issue. The client is generated for it, but nothing needs
  to call it yet.
- `planned`: it is tracked by the given roadmap issue. The [roadmap](https://github.com/smoketurner/terraform-provider-apple/issues/17)
  lists the issues.
- `out_of_scope`: it will not be covered, for the given reason.

The client in `internal/asc/gen` is generated with [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) for
the `covered` and `in_progress` operations only. `make generate` writes the generator configuration from
`coverage.yaml` and regenerates the client. Two hand-written files shape the output:

- `internal/asc/gen/overlay.yaml` renames generated types whose default names collide.
- `internal/asc/gen/datetime.go` defines `DateTime`, the type used for the spec's `date-time` strings, because App
  Store Connect writes UTC offsets without a colon (`+0000`), which Go's RFC 3339 parser rejects.

The rest of `internal/asc` is hand-written: JWT signing (`auth.go`), and retries, error decoding and pagination
(`client.go`).

To start on an operation, change it from `planned` to `in_progress` and run `make generate`. Once the provider calls
it, mark it `covered` and list what uses it. `make api-coverage` checks that:

- every spec operation is mapped once, and the manifest names no operation the spec lacks;
- the generator configuration matches `coverage.yaml`, and the generated client has a method for each generated
  operation;
- the provider calls the generated method of every `covered` operation, and every name under `by` is registered by
  the provider.

The **API spec watch** workflow runs weekly. It downloads Apple's latest spec, compares it with the vendored spec and
the manifest, and opens or updates an `api-spec` issue for each new operation and each new request field or query
parameter on a `covered` or `in_progress` operation. To move to a newer spec, run
`go run ./internal/apispec/cmd/apispec fetch -o api/openapi.oas.json`, copy the version and checksum it prints into
`internal/apispec/pin.go`, update `coverage.yaml` until `make api-coverage` passes, and run `make generate`.

## Tests

`make test` runs every test, including the Terraform acceptance tests, against an in-memory fake of the App Store
Connect API. There are no live tests: the fake encodes the behavior Apple documents, so keep it faithful to the
documentation.
