# molt - build, test and proof targets.
#
# The universal one-liner, if you have no make:
#
#	go build -o molt ./cmd/molt
#
# Everything below is convenience on top of that.

# Pinned so the reproducible-build hashes mean something. Override on the
# command line to build for another platform.
GO          ?= go
GOTOOLCHAIN ?= go1.27.0
GOOS        ?= $(shell $(GO) env GOOS)
GOARCH      ?= $(shell $(GO) env GOARCH)
BIN         ?= molt

# The flags that make the output byte-identical between builds:
#   -trimpath        strips absolute paths from the binary
#   -buildvcs=false  stops Go embedding the git commit and dirty flag
#   -s -w            drops the symbol table and DWARF
#   -buildid=        clears the build id, which otherwise varies
# CGO_ENABLED=0 keeps the host C toolchain out of the result entirely.
REPRO_ENV   = CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) GOTOOLCHAIN=$(GOTOOLCHAIN)
REPRO_FLAGS = -trimpath -buildvcs=false -ldflags "-s -w -buildid="

.PHONY: all build test vet fmt lint repro deps-proof clean demo help

all: fmt vet test build

## build: compile the binary
build:
	$(GO) build -o $(BIN) ./cmd/molt

## test: run every test, including the end-to-end apply proof
test:
	$(GO) test ./...

## vet: run the standard vet suite
vet:
	$(GO) vet ./...

## fmt: format every file
fmt:
	$(GO) fmt ./...

## lint: fail if anything is unformatted
lint:
	@unformatted=$$(gofmt -l . | grep -v '^testdata/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi
	@echo "gofmt clean"

## repro: build twice and prove the two artifacts are byte-identical
repro:
	@sh scripts/reproducible-build.sh

## deps-proof: regenerate deps-proof.txt
deps-proof:
	@sh scripts/deps-proof.sh > deps-proof.txt
	@echo "wrote deps-proof.txt"

## demo: run molt against the bundled fixtures
demo: build
	@echo "=== a module molt can migrate completely ==="
	@./$(BIN) testdata/tidy-app
	@echo
	@echo "=== the patch it would apply ==="
	@./$(BIN) -diff testdata/tidy-app

clean:
	rm -f $(BIN) $(BIN).exe molt-build-1 molt-build-2

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
