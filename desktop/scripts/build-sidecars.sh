#!/usr/bin/env bash
set -euo pipefail

desktop_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repository_root="$(cd "${desktop_root}/.." && pwd)"
sidecar_dir="${desktop_root}/build/sidecars"
source_head="$(git -C "${repository_root}" rev-parse HEAD)"
build_identity="0.1.0-dev+g${source_head:0:7}"
ldflags="-X github.com/lazyun/devbridge/internal/buildinfo.ProductVersion=0.1.0-dev -X github.com/lazyun/devbridge/internal/buildinfo.GitCommit=${source_head}"

mkdir -p "${sidecar_dir}"
(
  cd "${repository_root}"
  go build -trimpath -ldflags "${ldflags}" -o "${sidecar_dir}/devbridge" ./cmd/devbridge
  go build -trimpath -ldflags "${ldflags}" -o "${sidecar_dir}/devbridge-mcp" ./cmd/devbridge-mcp
  go build -trimpath -ldflags "${ldflags}" -o "${sidecar_dir}/devbridge-local-mcp" ./cmd/devbridge-local-mcp
)
echo "Prepared Dev Bridge sidecars ${build_identity} in ${sidecar_dir}"
