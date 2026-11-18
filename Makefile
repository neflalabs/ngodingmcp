BINARY := ngodingmcp
PKG := ngodingmcp/internal/buildinfo

# The version comes from internal/buildinfo (CalVer line + git commit, which
# go build embeds automatically). Override the CalVer line for a release with:
#   make release VERSION=v2026.12.0
# When a matching git tag exists (v20xx.xx.xx), use it automatically when
# VERSION is not given explicitly; untagged checkouts fall back to buildinfo.
VERSION ?= $(shell git describe --tags --match 'v20*' --exact-match 2>/dev/null || echo)
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
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe .
	@ls -lh dist/

# Build, then print the version of the new binary.
version: build
	./$(BINARY) version

test:
	go test ./...

clean:
	rm -rf dist $(BINARY)
