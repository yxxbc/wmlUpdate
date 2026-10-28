SHELL := /bin/sh
BINARY := wml-update
MODULE := github.com/yxxbc/wmlUpdate
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT) -X $(MODULE)/internal/buildinfo.Date=$(DATE)

.PHONY: all build test race vet check fmt lint clean dist install snapshot \
        cross-windows cross-darwin cross-linux

all: check build

build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=true -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/wml-update

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

lint: vet

check: fmt test vet
	@git diff --exit-code -- '*.go' || (echo "gofmt changed files; commit the result" >&2; exit 1)

install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/wml-update

cross-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)_windows_amd64.exe ./cmd/wml-update
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)_windows_arm64.exe ./cmd/wml-update

cross-darwin:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)_darwin_amd64 ./cmd/wml-update
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)_darwin_arm64 ./cmd/wml-update

cross-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)_linux_amd64 ./cmd/wml-update
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)_linux_arm64 ./cmd/wml-update

dist: cross-windows cross-darwin cross-linux

snapshot:
	goreleaser release --snapshot --clean

docs:
	@echo "Install docs dependencies: pip install -r requirements-docs.txt"
	@echo "Preview: mkdocs serve"
	@echo "Build:   mkdocs build --strict"

clean:
	rm -rf bin dist site
