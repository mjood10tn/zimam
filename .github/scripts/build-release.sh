#!/usr/bin/env bash
# Builds a signed release into dist/ at the top of the repository (design §3):
# the static agents for amd64 and arm64, the panel tarball, install.sh with
# the release key and base URL filled in, manifest.json and its signature.
# release.yml runs it on a tag; e2e and local checks run it with a throwaway
# key. It needs go, php with composer, npm, git, GNU tar, gzip and GNU date.
#
# Usage: build-release.sh <version> <base-url> <private-key> [--e2e]
set -Eeuo pipefail

usage() {
	echo "usage: build-release.sh <version> <base-url> <private-key> [--e2e]" >&2
	exit 2
}

die() {
	echo "build-release: $*" >&2
	exit 1
}

[[ $# -eq 3 || ($# -eq 4 && $4 == --e2e) ]] || usage
version=$1
base_url=$2
key=$3
e2e=${4:-}

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

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
dist=$root/dist
installer=$root/installer/install.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Every timestamp in the panel tarball is the release commit's, so one
# commit always gives the same tarball.
epoch=$(git -C "$root" log -1 --format=%ct)

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
panel=$work/panel
mkdir "$panel"
# Only committed files, so a local .env, database or node_modules can
# never reach the tarball.
git -C "$root" archive --format=tar HEAD:panel | tar -x -C "$panel"
(
	cd "$panel"
	composer install --no-dev --classmap-authoritative --no-interaction --no-progress --prefer-dist
	# No package install scripts run where a signing key is on disk; the
	# asset build needs none (composer.json's setup script does the same).
	npm ci --ignore-scripts --no-audit --no-fund
	npm run build
	# storage/ is linked in from the server's shared/ (design §4 step 6), and
	# symlink-swap can't replace a directory, so the release has none.
	rm -rf node_modules tests storage .env .env.*
)
tarball=zimam-panel-$version.tar.gz
(cd "$panel" && find . -mindepth 1 -printf '%P\n' | LC_ALL=C sort) >"$work/panel-files"
tar --create --format=gnu --no-recursion --verbatim-files-from --hard-dereference \
	--owner=0 --group=0 --numeric-owner --mtime="@$epoch" --mode='u+rwX,go+rX,go-w' \
	-C "$panel" -T "$work/panel-files" | gzip -9 -n >"$dist/$tarball"

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
"$work/release" manifest -version "$version" -expires "$expires" -composer "$panel/composer.json" \
	-out "$dist/manifest.json" \
	"$dist/zimam-agent-linux-amd64" "$dist/zimam-agent-linux-arm64" "$dist/$tarball" "$dist/install.sh"
"$work/release" sign -key "$key" "$dist/manifest.json"
printf '%s\n' "$public_pem" >"$work/release-key.pub"
"$work/release" verify -key "$work/release-key.pub" -agent-version "$version" "$dist"
ls -l "$dist"
