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
	# v0.8.1 can't read Go 1.27.2 export data; this is the same master commit
	# test.yml pins. Move both back to a release once one includes the fix.
	$(TOOLRUN) honnef.co/go/tools/cmd/staticcheck@v0.7.0-0.dev.0.20261009230814-452d5bb86b45 ./...
	$(TOOLRUN) golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

check: vet race lint

build:
	mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/synk

doctor: build
	SYNK_DIR=.data ./$(BIN) init --name "dev"
	SYNK_DIR=.data ./$(BIN) doctor
