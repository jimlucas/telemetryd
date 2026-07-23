SHELL := /bin/sh

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

.PHONY: all build test vet race check verify fmt tidy clean run sim

all: check build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/telemetryd ./cmd/telemetryd
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/telemetrysim ./cmd/telemetrysim

test:
	go test ./...

vet:
	go vet ./...

race:
	go test -race -p 1 ./...

check: test vet

verify: check race build

fmt:
	gofmt -w $$(find . -type f -name '*.go')

tidy:
	go mod tidy

run:
	go run ./cmd/telemetryd

sim:
	go run ./cmd/telemetrysim

clean:
	rm -rf bin
