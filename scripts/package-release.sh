#!/bin/bash
# Package binaries produced by build.sh, without rebuilding or downloading data.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p build
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
for file in geoip.dat geosite.dat geoip.db geosite.db config.json custom_inbound.json custom_outbound.json dns.json route.json; do
    cp "example/$file" "$stage/"
done
cp scripts/manager/{V2bX.sh,V2bX.service,initconfig.sh,LICENSE} "$stage/"
cp scripts/observe-memory.sh "$stage/"
repo_dir=$PWD
for target in amd64 arm64; do
    case "$target" in amd64) asset=64 ;; arm64) asset=arm64-v8a ;; esac
    cp "build/V2bX-$target" "$stage/V2bX"
    chmod +x "$stage/V2bX"
    rm -f "build/V2bX-linux-$asset.zip"
    (cd "$stage" && zip -q "$repo_dir/build/V2bX-linux-$asset.zip" ./*)
done
cp install.sh build/install.sh
(cd build && shasum -a 256 V2bX-linux-64.zip V2bX-linux-arm64-v8a.zip install.sh > SHA256SUMS)
echo 'Release assets: build/V2bX-linux-*.zip, build/install.sh, build/SHA256SUMS'
