BINARY := ngodingmcp
PKG := ngodingmcp/internal/buildinfo

# The version comes from internal/buildinfo (CalVer line + git commit, which
# go build embeds automatically). Release flow:
#   make release VERSION=v2026.12.0
# or `git tag v2026.12.0` at the release commit, then `make build`. Without
# either, every build stamps a development version derived from today's date
# (v<YYYY.MM.DD>-dev), so builds never silently share a stale CalVer line.
# Override with `make release VERSION=...` or by exporting VERSION.
GIT_TAG := $(shell git describe --tags --match 'v20*' --exact-match 2>/dev/null)
ifeq ($(strip $(GIT_TAG)),)
VERSION ?= v$(shell date -u +%Y.%m.%d)-dev
else
VERSION ?= $(GIT_TAG)
endif
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w -X $(PKG).buildTime=$(BUILD_TIME)
ifneq ($(strip $(VERSION)),)
LDFLAGS += -X $(PKG).version=$(VERSION)
endif
GOFLAGS := -trimpath

.PHONY: build release clean test version

# Static, self-contained binary (no libc dependency).
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Cross-compiled binaries in dist/.
release:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-apple-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-apple-arm64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe .
	@ls -lh dist/

# Build, then print the version of the new binary.
version: build
	./$(BINARY) version

test:
	go test ./...

clean:
	rm -rf dist $(BINARY)
