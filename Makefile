.PHONY: build test vet fmt check clean

build:
	go build -o gnpm ./cmd/gnpm

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

check: fmt vet test

clean:
	rm -f gnpm
	go clean
