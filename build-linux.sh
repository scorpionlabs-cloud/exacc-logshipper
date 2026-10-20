#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
for arch in amd64 arm64; do
  mkdir -p "dist/linux-$arch"
  for app in exacc-collect exacc-shipper exacc-receiver; do
    echo "Building linux/$arch $app"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
      -ldflags='-s -w' -o "dist/linux-$arch/$app" "./cmd/$app"
  done
done
