export GOTOOLCHAIN := local

.PHONY: check vet test lint build build-universal release-snapshot notices

check: vet test lint

vet:
	go vet ./...

test:
	go test -race ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed; skipped (CI runs it)"; fi

build:
	go build -o bin/sapu ./cmd/sapu

# Universal (arm64 + amd64) binary at bin/sapu, built with the Go toolchain and
# lipo. The two single-arch binaries stay next to it in bin/ (git-ignored).
build-universal:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o bin/sapu-arm64 ./cmd/sapu
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/sapu-amd64 ./cmd/sapu
	lipo -create -output bin/sapu bin/sapu-arm64 bin/sapu-amd64

# Full release build into dist/ without publishing or signing.
release-snapshot:
	@if command -v goreleaser >/dev/null 2>&1; then goreleaser release --snapshot --clean --skip=sign,publish; else echo "goreleaser not installed; skipped (brew install goreleaser)"; fi

# Rewrite THIRD_PARTY_NOTICES (license texts of the Go modules in the binary).
# Run it after changing go.mod and commit the result; `make test` fails if the
# file is stale.
notices:
	go run ./internal/tools/notices
