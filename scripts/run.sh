#!/usr/bin/env bash
set -euo pipefail

CONFIG="${1:-config.yaml}"

echo "Building..."
go build -o localrouter ./cmd/localrouter

echo "Running with config: $CONFIG"
./localrouter -config "$CONFIG"
