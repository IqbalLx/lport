#!/bin/sh
# Builds a release tarball: dist/release/lport-<version>-<platform>.tar.gz
#
#   scripts/build-release.sh <version> [platform]
#
# platform is one of darwin-arm64, darwin-x64, linux-x64, linux-arm64 (default: this machine).
# install.sh expects exactly this layout: a tarball holding a single `lport` binary.
set -eu

version="${1:?usage: scripts/build-release.sh <version> [platform]}"
platform="${2:-}"

if [ -z "$platform" ]; then
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64) arch=x64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
  platform="$os-$arch"
fi

case "$platform" in
  darwin-arm64) goos=darwin; goarch=arm64 ;;
  darwin-x64) goos=darwin; goarch=amd64 ;;
  linux-x64) goos=linux; goarch=amd64 ;;
  linux-arm64) goos=linux; goarch=arm64 ;;
  *) echo "unsupported platform: $platform" >&2; exit 1 ;;
esac

cd "$(dirname "$0")/.."
out=dist/release
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
  -ldflags "-s -w -X main.version=$version" -o "$work/lport" .

mkdir -p "$out"
tar -czf "$out/lport-$version-$platform.tar.gz" -C "$work" lport
echo "$out/lport-$version-$platform.tar.gz"
