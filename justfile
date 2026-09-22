set shell := ["bash", "-euo", "pipefail", "-c"]

# List available recipes
default:
    @just --list

# Build the binary into .tmp/
build:
    go build -o .tmp/grill-tui ./cmd/grill-tui

# Run the worksheet, optionally from a starting number, e.g. `just run 12`
run *args:
    go run ./cmd/grill-tui {{ args }}

# Run the full test suite
test:
    go test ./...

# Format all Go sources
fmt:
    go fmt ./...

# Check formatting, run vet, and run the tests
check:
    @unformatted="$(gofmt -l .)"; if [ -n "$unformatted" ]; then echo "needs gofmt:"; echo "$unformatted"; exit 1; fi
    go vet ./...
    go test ./...

# Install grill-tui from this source tree and report where it landed
install:
    go install ./cmd/grill-tui
    @destination="$(go env GOBIN)"; if [ -z "$destination" ]; then destination="$(go env GOPATH)/bin"; fi; echo "installed grill-tui to $destination"

# Remove build artifacts
clean:
    rm -rf .tmp
