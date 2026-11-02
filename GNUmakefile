default: fmt lint build test

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w -e .
	terraform fmt -recursive examples/

# Regenerates the API client in internal/asc/gen from coverage.yaml and the
# vendored spec, then the documentation in docs/.
generate:
	go run ./internal/apispec/cmd/apispec gencfg -o internal/asc/gen/oapi-codegen.yaml
	cd internal/asc/gen && go tool -modfile=../../../tools/go.mod oapi-codegen \
		-config oapi-codegen.yaml -o asc.gen.go ../../../api/openapi.oas.json
	cd tools && go generate -tags generate ./...

# Runs every test, including acceptance tests, against the in-memory fake
# App Store Connect API. Requires terraform on PATH.
test:
	go test -cover -timeout=10m ./...

# Checks coverage.yaml and the generated client against the vendored spec.
api-coverage:
	go test ./internal/apispec/...

.PHONY: default build install lint fmt generate test api-coverage
