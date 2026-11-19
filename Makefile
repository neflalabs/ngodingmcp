BINARY := ngodingmcp
PKG := ngodingmcp/internal/buildinfo

# The version comes from internal/buildinfo: <CalVer>+<commit>[.dirty].
# The commit is embedded by go build itself. The CalVer line is
#   - the exact v20* git tag on HEAD, if there is one;
#   - VERSION when given: make release VERSION=v2026.12.0;
#   - otherwise v<commit date>-dev, worked out by buildinfo, so `make build`
#     and a plain `go build -o ngodingmcp .` print the same version.
GIT_TAG := $(shell git describe --tags --match 'v20*' --exact-match 2>/dev/null)
VERSION ?= $(GIT_TAG)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w -X $(PKG).buildTime=$(BUILD_TIME)
ifneq ($(strip $(VERSION)),)
LDFLAGS += -X $(PKG).version=$(VERSION)
endif
GOFLAGS := -trimpath

# Pinned here and in .github/workflows/ci.yml; keep both in step.
STATICCHECK_VERSION := v0.8.2

# `ngodingmcp -d` writes this unit with ExecStart="<absolute binary path>" serve.
UNIT_FILE := $(or $(XDG_CONFIG_HOME),$(HOME)/.config)/systemd/user/ngodingmcp.service

.PHONY: build release clean test check version

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

# The same checks as CI: gofmt, vet, staticcheck and the race-enabled tests.
check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...
	go test -count=1 -race ./...

# Removes dist/ and ./ngodingmcp, but keeps the binary while the systemd user
# service runs it, so `make clean` cannot break `ngodingmcp -d`.
clean:
	rm -rf dist
	@if grep -qsF 'ExecStart="$(CURDIR)/$(BINARY)"' "$(UNIT_FILE)"; then \
		echo "kept ./$(BINARY): ngodingmcp.service runs it (make build replaces it in place)"; \
	else \
		rm -f $(BINARY); echo "rm -f $(BINARY)"; \
	fi
