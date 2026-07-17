.PHONY: build install test vet check

build:
	go build -o bin/tarakan ./cmd/tarakan

# Install to ~/.local/bin (or $GOBIN / $TARAKAN_INSTALL_DIR).
install:
	@mkdir -p "$${TARAKAN_INSTALL_DIR:-$${GOBIN:-$${HOME}/.local/bin}}"
	go build -o "$${TARAKAN_INSTALL_DIR:-$${GOBIN:-$${HOME}/.local/bin}}/tarakan" ./cmd/tarakan
	@echo "installed tarakan → $${TARAKAN_INSTALL_DIR:-$${GOBIN:-$${HOME}/.local/bin}}/tarakan"

test:
	go test ./...

vet:
	go vet ./...

check: test vet build
