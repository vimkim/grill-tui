#!/usr/bin/env bash

set -euo pipefail

install_directory="$(mktemp -d)"
cleanup() {
    rm -rf "$install_directory"
}
trap cleanup EXIT

GOBIN="$install_directory" go install ./cmd/grill-tui
"$install_directory/grill-tui" config defaults >/dev/null
