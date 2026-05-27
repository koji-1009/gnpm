.PHONY: build test vet fmt check clean conformance

build:
	go build -o gnpm ./cmd/gnpm

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: fmt vet test

# Differential conformance: run npm, pnpm, and gnpm on the same fixtures and
# compare their resolved version sets 1:1. Needs npm, pnpm, and network, and
# must run outside a restrictive sandbox. Pass flags via ARGS, e.g.
#   make conformance ARGS="--fixture lodash"
conformance:
	go run ./tools/conformance $(ARGS)

clean:
	rm -f gnpm
	go clean
