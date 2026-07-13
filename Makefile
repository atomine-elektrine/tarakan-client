.PHONY: build test vet check

build:
	go build -o bin/tarakan ./cmd/tarakan

test:
	go test ./...

vet:
	go vet ./...

check: test vet build
