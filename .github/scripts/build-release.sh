#!/usr/bin/env bash
# Builds a signed release into dist/ at the top of the repository (design §3):
# the static agents for amd64 and arm64, install.sh with the release key and
# base URL filled in, the panel tarball from build-panel.sh, manifest.json and
# its signature. release.yml runs it on a tag; e2e and local checks run it
# with a throwaway key.
#
# The key is on disk while this runs, so it runs nothing but Go, git and the
# shell: no Composer, npm or PHP. The panel tarball is only hashed and
# copied, never unpacked. It needs go, git and GNU date.
#
# Usage: build-release.sh <version> <base-url> <private-key> <panel-tarball> [--e2e]
set -Eeuo pipefail

usage() {
	echo "usage: build-release.sh <version> <base-url> <private-key> <panel-tarball> [--e2e]" >&2
	exit 2
}

die() {
	echo "build-release: $*" >&2
	exit 1
}

[[ $# -eq 4 || ($# -eq 5 && $5 == --e2e) ]] || usage
version=$1
base_url=$2
key=$3
panel_tarball=$4
e2e=${5:-}

# The version names files here and directories on servers; the release
# tool then checks it is full SemVer.
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
	die "version $version isn't <major>.<minor>.<patch>[-<pre-release>]"
# The base URL ends up in install.sh, which runs as root, and in the agent:
# no quotes, spaces, \$, backticks, backslashes or &, and no trailing slash.
[[ $base_url =~ ^(https?|file)://[A-Za-z0-9._~:/@%+-]*[A-Za-z0-9._~:@%+-]$ ]] ||
	die "base URL $base_url isn't a plain http(s) or file URL without a trailing slash"
[[ -f $key ]] || die "no private key at $key"
key=$(realpath "$key")
# The tarball comes from a job that ran other people's code: take exactly
# the file build-panel.sh names, and nothing else.
tarball=zimam-panel-$version.tar.gz
[[ $(basename "$panel_tarball") == "$tarball" && -f $panel_tarball && ! -L $panel_tarball ]] ||
	die "the panel tarball must be a regular file named $tarball, not $panel_tarball"
panel_tarball=$(realpath "$panel_tarball")

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
dist=$root/dist
installer=$root/installer/install.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

rm -rf "$dist"
mkdir -p "$dist"

echo "== release tool"
(cd "$root/agent" && go build -o "$work/release" ./tools/release)
public_raw=$("$work/release" pubkey -key "$key" -raw)
public_pem=$("$work/release" pubkey -key "$key")

echo "== agents"
tags=()
[[ -z $e2e ]] || tags=(-tags e2e)
pkg=zimam/agent/internal/release
ldflags="-s -w -X $pkg.Version=$version -X $pkg.PublicKey=$public_raw -X $pkg.BaseURL=$base_url"
for arch in amd64 arm64; do
	(cd "$root/agent" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
		go build -trimpath -buildvcs=true "${tags[@]}" -ldflags "$ldflags" \
		-o "$dist/zimam-agent-linux-$arch" ./cmd/zimam-agent)
done

echo "== panel"
cp "$panel_tarball" "$dist/$tarball"
# panel_php_min (D7) comes from the committed composer.json and lock, never
# from the tarball, so the panel's build can't choose a signed field.
git -C "$root" show HEAD:panel/composer.json >"$work/composer.json"
git -C "$root" show HEAD:panel/composer.lock >"$work/composer.lock"

echo "== install.sh"
[[ -f $installer ]] || die "installer/install.sh is missing"
IFS= read -r -d '' script <"$installer" || true
for placeholder in @@RELEASE_PUBLIC_KEY_PEM@@ @@RELEASE_BASE_URL@@; do
	[[ $script == *"$placeholder"* ]] || die "installer/install.sh has no $placeholder"
done
script=${script//@@RELEASE_PUBLIC_KEY_PEM@@/"$public_pem"}
script=${script//@@RELEASE_BASE_URL@@/"$base_url"}
[[ $script != *@@RELEASE_* ]] || die "installer/install.sh has a placeholder this script doesn't fill"
printf '%s' "$script" >"$dist/install.sh"
chmod 0755 "$dist/install.sh"

echo "== manifest"
expires=$(date -u -d '+30 days' +%Y-%m-%dT%H:%M:%SZ)
"$work/release" manifest -version "$version" -expires "$expires" -composer "$work/composer.json" \
	-out "$dist/manifest.json" \
	"$dist/zimam-agent-linux-amd64" "$dist/zimam-agent-linux-arm64" "$dist/$tarball" "$dist/install.sh"
"$work/release" sign -key "$key" "$dist/manifest.json"
printf '%s\n' "$public_pem" >"$work/release-key.pub"
"$work/release" verify -key "$work/release-key.pub" -agent-version "$version" "$dist"
ls -l "$dist"
