#!/usr/bin/env bash
set -euo pipefail

desktop_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${desktop_root}"
"${desktop_root}/scripts/build-sidecars.sh"

if ! command -v wails >/dev/null 2>&1; then
  echo "Wails v2 CLI is required: install it explicitly, then rerun this script." >&2
  exit 1
fi

wails build "$@"
if [[ "$(go env GOOS)" == "darwin" ]]; then
  resource_bin="${desktop_root}/build/bin/devbridge-desktop.app/Contents/Resources/bin"
  mkdir -p "${resource_bin}"
  cp "${desktop_root}/build/sidecars/devbridge" "${resource_bin}/devbridge"
  cp "${desktop_root}/build/sidecars/devbridge-mcp" "${resource_bin}/devbridge-mcp"
  echo "Packaged sidecars in ${resource_bin}"
else
  echo "Sidecars are ready in build/sidecars; copy them to the platform package resources/bin directory."
fi
