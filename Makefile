# Top-level build. `make` builds the guests into root/bin and the emu and
# lethed into bin/. The real work is done by the go command and tools/;
# this file only checks the toolchain and names the usual steps.

GO ?= go

# go.mod asks for a newer Go than most distros ship. Go 1.21 and later
# fetch it themselves when GOTOOLCHAIN allows it, but Debian, Ubuntu and
# others set GOTOOLCHAIN=local in their go.env. Allow the download unless
# the caller chose otherwise in the environment.
GOTOOLCHAIN ?= auto
export GOTOOLCHAIN

.PHONY: all toolchain root emu run test check kernel clean help

all: root emu

help:
	@echo "make            build the guests (root/bin) and bin/emu, bin/lethed"
	@echo "make run        start the emu with an interactive sh"
	@echo "make test       unit and acceptance tests"
	@echo "make check      gofmt and go vet, host and guests"
	@echo "make kernel     the native kernel under QEMU (needs TamaGo, kernel/README.md)"
	@echo "make clean      remove bin/ and root/bin/"

toolchain:
	@command -v $(GO) >/dev/null 2>&1 || { \
	  echo "make: go is not installed. Get Go 1.21 or later from https://go.dev/dl"; exit 1; }
	@$(GO) list -m >/dev/null || { \
	  echo "make: this tree needs the Go named in go.mod. With Go 1.21 or later and"; \
	  echo "      GOTOOLCHAIN=auto the go command downloads it; older Go cannot."; exit 1; }

root: toolchain
	$(GO) run ./tools/build

emu: toolchain
	$(GO) build -o bin/ ./cmd/emu ./cmd/lethed

run: all
	bin/emu

test: root
	$(GO) test -race $$($(GO) list ./... | grep -v /cmd/emu)
	$(GO) test -timeout 30m ./cmd/emu

check: toolchain
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "make: gofmt needed"; exit 1; }
	$(GO) vet ./...
	GOOS=wasip1 GOARCH=wasm $(GO) vet ./sdk/... ./appl/...

kernel: root
	$(GO) run ./tools/kernel -corpus

clean:
	rm -rf bin root/bin
