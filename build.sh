#!/bin/bash
# Builds the static x86_64 Linux binary (no libc, no shared libraries) into dist/xivlite.
set -eu
cd "$(dirname "$0")"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/xivlite .
cp xivlite-wrap install.sh config.example README.md dist/
chmod 755 dist/xivlite dist/xivlite-wrap dist/install.sh
echo "dist/ is ready: $(cd dist && ls | tr '\n' ' ')"
