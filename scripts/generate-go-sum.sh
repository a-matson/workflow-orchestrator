#!/usr/bin/env bash
# generate-go-sum.sh — generates go.sum by downloading all dependencies
# Run this once after cloning: ./scripts/generate-go-sum.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$SCRIPT_DIR/../backend"

echo "Generating go.sum for backend..."
cd "$BACKEND_DIR"

# Download and verify all modules
go mod download
go mod verify
go mod tidy

echo "go.sum generated successfully at $BACKEND_DIR/go.sum"
echo "Module count: $(wc -l < go.sum) entries"
