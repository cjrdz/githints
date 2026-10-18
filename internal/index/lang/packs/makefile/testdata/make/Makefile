GO ?= go
BIN := githints

.PHONY: build test lint

build:
	$(GO) build -o $(BIN) .

test: build
	$(GO) test ./...

lint:
	golangci-lint run ./...

%.o: %.c
	cc -c $<

include scripts/release.mk
