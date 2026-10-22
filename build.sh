#!/bin/bash
# Builds the static x86_64 Linux binary (no libc, no shared libraries) into dist/xivlite, and the
# release assets: dist/xivlite-linux-x86_64.tar.gz and dist/SHA256SUMS.
set -eu
cd "$(dirname "$0")"
rm -rf dist && mkdir -p dist/xivlite
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/xivlite/xivlite .
cp xivlite-wrap install.sh config.example README.md LICENSE dist/xivlite/
chmod 755 dist/xivlite/xivlite dist/xivlite/xivlite-wrap dist/xivlite/install.sh
tar -C dist --owner=0 --group=0 -czf dist/xivlite-linux-x86_64.tar.gz xivlite
(cd dist && sha256sum xivlite-linux-x86_64.tar.gz > SHA256SUMS)
echo "built $(dist/xivlite/xivlite version): dist/xivlite/, dist/xivlite-linux-x86_64.tar.gz, dist/SHA256SUMS"
