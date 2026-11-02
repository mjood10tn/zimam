#!/usr/bin/env bash
# Builds the panel tarball, zimam-panel-<version>.tar.gz, into <out-dir>
# (design §3). It runs Composer, npm and the code they install, so it must
# never run where a signing key is: release.yml runs it in a job with no
# secrets, and build-release.sh signs the tarball it produces. It needs php
# with composer, npm, git, GNU tar and gzip.
#
# Usage: build-panel.sh <version> <out-dir>
set -Eeuo pipefail

usage() {
	echo "usage: build-panel.sh <version> <out-dir>" >&2
	exit 2
}

die() {
	echo "build-panel: $*" >&2
	exit 1
}

[[ $# -eq 2 ]] || usage
version=$1
out=$2
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
	die "version $version isn't <major>.<minor>.<patch>[-<pre-release>]"

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Every timestamp in the tarball is the release commit's, so one commit
# always gives the same tarball.
epoch=$(git -C "$root" log -1 --format=%ct)

mkdir -p "$out"
echo "== panel"
panel=$work/panel
mkdir "$panel"
# Only committed files, so a local .env, database or node_modules can
# never reach the tarball.
git -C "$root" archive --format=tar HEAD:panel | tar -x -C "$panel"
(
	cd "$panel"
	composer install --no-dev --classmap-authoritative --no-interaction --no-progress --prefer-dist
	# The asset build needs no package install scripts (composer.json's
	# setup script does the same).
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
	-C "$panel" -T "$work/panel-files" | gzip -9 -n >"$out/$tarball"
ls -l "$out/$tarball"
