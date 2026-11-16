package apispec

// The vendored spec, api/openapi.oas.json. To move to a newer spec, run
// `go run ./internal/apispec/cmd/apispec fetch -o api/openapi.oas.json`, copy
// the version and checksum it prints here, update coverage.yaml until
// `make api-coverage` passes, and run `make generate`.
const (
	PinnedVersion = "4.6"
	PinnedSHA256  = "74732362a91236dcb66eb1620f8be18c2b6060771269b483eab578473bb32763"
)
