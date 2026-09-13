# Quality gates. `make ci` is exactly what GitHub Actions runs; `make hooks`
# wires the same gates into this clone (pre-commit: quick, pre-push: ci).
#
# The PocketBook SDK container exports GOARCH=arm / CC=<arm clang> globally,
# so every host-side gate pins its own environment. The `arm` gate builds
# the real InkView binary and only runs where the SDK toolchain exists.
GO ?= go
GOFMT := $(shell $(GO) env GOROOT)/bin/gofmt
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@2026.2.1
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0
TESTFLAGS ?=
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
ARM_CC ?= /opt/pocketbook-sdk/usr/bin/arm-obreey-linux-gnueabi-clang

HOSTENV := GOOS=linux GOARCH=amd64 GOARM= CC=gcc CGO_ENABLED=1
ARMENV  := GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=1 CC=$(ARM_CC)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: ci quick fmt fmt-check vet lint tidy-check test build arm vuln hooks clean

ci: fmt-check vet lint tidy-check test build vuln
ifneq ($(wildcard $(ARM_CC)),)
ci: arm
endif

quick: fmt-check vet

fmt:
	$(GOFMT) -w .

fmt-check:
	@set -e; out="$$($(GOFMT) -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

vet:
	$(HOSTENV) $(GO) vet ./...

lint:
	$(HOSTENV) $(GO) run $(STATICCHECK) ./...

tidy-check:
	$(GO) mod tidy -diff

test:
	$(HOSTENV) $(GO) test $(TESTFLAGS) ./...

# amd64 CLI build (the same source the tests exercise).
build:
	$(HOSTENV) $(GO) build -o /dev/null .

# The on-device InkView binary. Needs the PocketBook SDK toolchain.
arm:
	@test -x "$(ARM_CC)" || { echo "arm: $(ARM_CC) not found (run inside the SDK container)"; exit 1; }
	mkdir -p dist
	$(ARMENV) $(GO) build -ldflags="$(LDFLAGS)" -o dist/pocketbeam.app .
	@m=$$(od -An -tx1 -j18 -N2 dist/pocketbeam.app | tr -d ' '); test "$$m" = "2800" || { echo "arm: e_machine $$m is not ARM"; exit 1; }
	@echo "built dist/pocketbeam.app ($(VERSION))"

vuln:
	$(HOSTENV) $(GO) run $(GOVULNCHECK) ./...

hooks:
	git config core.hooksPath .githooks
	@echo "hooks installed: pre-commit runs 'make quick', pre-push runs 'make ci'"

clean:
	rm -rf dist
