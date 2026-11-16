GO ?= go
BIN := bin/synk
# Analysis tools must be built with the same toolchain that compiles the module.
TOOLCHAIN := $(shell awk '/^toolchain/ {print $$2}' go.mod)
TOOLRUN := GOTOOLCHAIN=$(TOOLCHAIN) $(GO) run

.PHONY: test race check lint build vet fmt doctor

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

lint:
	test -z "$$(gofmt -l .)"
	$(TOOLRUN) honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
	$(TOOLRUN) golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

check: vet race lint

build:
	mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/synk

doctor: build
	SYNK_DIR=.data ./$(BIN) init --name "dev"
	SYNK_DIR=.data ./$(BIN) doctor
