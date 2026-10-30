#!/usr/bin/env bash

# Build the complete local Dev Bridge Desktop runtime set under <repo>/build:
#
#   build/bin/devbridge
#   build/bin/devbridge-mcp
#   build/bin/devbridge-local-mcp
#   build/Dev Bridge Desktop.app
#
# The packaged Desktop resolves these runtime executables from the bin
# directory adjacent to the application bundle, so the built app needs no
# permanent shell PATH configuration.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$ROOT/build"
BIN_DIR="$BUILD_DIR/bin"
DESKTOP_DIR="$ROOT/desktop"
WAILS_OUTPUT="$DESKTOP_DIR/build/bin"

source_head="$(git -C "$ROOT" rev-parse HEAD)"
ldflags="-X github.com/lazyun/devbridge/internal/buildinfo.ProductVersion=0.1.0-dev -X github.com/lazyun/devbridge/internal/buildinfo.GitCommit=${source_head}"

cd "$ROOT"

echo "[1/4] Building runtime binaries"
mkdir -p "$BIN_DIR"
go build -trimpath -ldflags "$ldflags" -o "$BIN_DIR/devbridge" ./cmd/devbridge
go build -trimpath -ldflags "$ldflags" -o "$BIN_DIR/devbridge-mcp" ./cmd/devbridge-mcp
go build -trimpath -ldflags "$ldflags" -o "$BIN_DIR/devbridge-local-mcp" ./cmd/devbridge-local-mcp

echo "[2/4] Verifying runtime binaries"
for executable in devbridge devbridge-mcp devbridge-local-mcp; do
  if [[ ! -x "$BIN_DIR/$executable" ]]; then
    echo "Required runtime executable was not built: $BIN_DIR/$executable" >&2
    exit 1
  fi
  echo "$executable -> $BIN_DIR/$executable"
done

if ! command -v wails >/dev/null 2>&1; then
  echo "Wails CLI not found in PATH. Install the project-compatible Wails CLI before building Desktop." >&2
  exit 1
fi

echo "[3/4] Building Desktop"
# Build-time convenience only: the Wails frontend toolchain may invoke the
# freshly built CLI. The packaged app never depends on this PATH afterwards.
export PATH="$BIN_DIR:$PATH"
cd "$DESKTOP_DIR"
rm -rf "$WAILS_OUTPUT/Dev Bridge Desktop.app"
wails build -clean

echo "[4/4] Staging Desktop into root build"
if [[ "$(go env GOOS)" == "darwin" ]]; then
  built_bundle="$WAILS_OUTPUT/Dev Bridge Desktop.app"
  staged_bundle="$BUILD_DIR/Dev Bridge Desktop.app"
  if [[ ! -d "$built_bundle" ]]; then
    echo "Wails build did not produce $built_bundle" >&2
    exit 1
  fi
  rm -rf "$staged_bundle"
  mv "$built_bundle" "$staged_bundle"
  rm -rf "$WAILS_OUTPUT/Dev Bridge Desktop.app"
else
  echo "Non-macOS build: Wails output remains in $WAILS_OUTPUT" >&2
fi

echo "Desktop build complete: $BUILD_DIR"
