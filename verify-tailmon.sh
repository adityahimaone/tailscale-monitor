#!/bin/bash
# Verifikasi build tailscale-monitor di Mac: gofmt, vet, build.
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
cd ~/Development/tailscale-monitor || exit 1
echo "=== gofmt ==="
gofmt -l .
echo "gofmt exit: $?"
echo "=== go vet ==="
go vet ./...
echo "vet exit: $?"
echo "=== go build ==="
go build -o tailmon .
echo "build exit: $?"
ls -la tailmon 2>&1
echo "=== README auth section ==="
grep -n -i -A 15 "auth\|password" README.md | head -n 60
