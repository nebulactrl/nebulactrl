#!/usr/bin/env bash
# Builds the release archives into dist/ and writes dist/SHA256SUMS:
#
#   nebulactrl_<version>_<os>_<arch>.tar.gz        the CLI (os: linux, darwin; arch: amd64, arm64)
#   nebulactrl-agent_<version>_linux_<arch>.tar.gz the agent (arch: amd64, arm64)
#
# The control plane has no archive: it ships as the container image
# (ghcr.io/nebulactrl/nebulactrl), which also serves the agent binaries to the
# hosts it enrolls.
#
#   scripts/release/build-binaries.sh 0.2.0 [outdir]      # `make release-binaries VERSION=0.2.0`
#
# <version> has no leading "v". Every binary is static (CGO_ENABLED=0), built with
# -trimpath and stamped through -ldflags "-X main.version=<version>", like
# `make build-agents`. Go cross-compiles all four targets on one machine, so the
# release workflow runs this in one job.
set -euo pipefail

version="${1:?usage: build-binaries.sh <version> [outdir]}"
version="${version#v}"
out="${2:-dist}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

case "$version" in
[0-9]*.[0-9]*.[0-9]*) ;;
*) echo "build-binaries.sh: '$version' is not a semantic version (1.2.3)" >&2; exit 2 ;;
esac

rm -rf "$out"
mkdir -p "$out"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

ldflags="-s -w -X main.version=${version}"

# build <package dir> <binary name> <archive prefix> <os> <arch>
build() {
	local pkg="$1" bin="$2" prefix="$3" goos="$4" goarch="$5"
	local dir="$stage/${prefix}_${version}_${goos}_${goarch}"
	mkdir -p "$dir"
	echo "build  ${prefix}_${version}_${goos}_${goarch}"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$dir/$bin" "$pkg"
	cp LICENSE "$dir/"
	tar -C "$stage" -cf - "$(basename "$dir")" | gzip -9n >"$out/$(basename "$dir").tar.gz"
}

for arch in amd64 arm64; do
	build ./cmd/agent nebulactrl-agent nebulactrl-agent linux "$arch"
	for os in linux darwin; do
		build ./cmd/cli nebulactrl nebulactrl "$os" "$arch"
	done
done

(
	cd "$out"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -- *.tar.gz
	else
		shasum -a 256 -- *.tar.gz
	fi >SHA256SUMS
)
echo "wrote $(ls "$out" | wc -l | tr -d ' ') files to $out/"
