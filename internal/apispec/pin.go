package apispec

// The vendored spec, api/openapi.oas.json. To move to a newer spec, run
// `go run ./internal/apispec/cmd/apispec fetch -o api/openapi.oas.json`, copy
// the version and checksum it prints here, update coverage.yaml until
// `make api-coverage` passes, and run `make generate`.
const (
	PinnedVersion = "4.5.1"
	PinnedSHA256  = "7518d3a94a8bd701ac25c1c601b95b8f53aad92091affb15a2a9d331713dac1a"
)
