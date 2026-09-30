#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p build
version=${VERSION:-$(cat VERSION)}
for target in amd64 arm64; do
    echo "编译 Linux $target ($version)..."
    CGO_ENABLED=0 GOOS=linux GOARCH="$target" go build \
        -tags 'sing,xray,hysteria2,with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api' \
        -o "build/V2bX-$target" \
        -ldflags="-s -w -X github.com/InazumaV/V2bX/cmd.version=$version" main.go
done
bash scripts/package-release.sh
