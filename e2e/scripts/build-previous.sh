#!/usr/bin/env bash
# Builds the previous release for the upgrade run (scenarios/upgrade.sh):
# the newest tag before this commit (git describe on HEAD^, so a tagged
# commit gets the tag before its own), or E2E_PREVIOUS_TAG. The tag is
# checked out with git worktree add under $E2E_WORK, and built by that
# tree's own e2e/scripts/build.sh, unchanged: this tree's may rely on files
# the old one lacks. It runs with E2E_WORK set to
# $E2E_WORK/release-previous, so the old release, its throwaway key, its
# contract copy and binaries land there, and with E2E_VERSION set to the
# tag's version (E2E_PREVIOUS_VERSION overrides it). Its key,
# release-previous/keys/release-key, then signs this tree's build too
# (build.sh's E2E_RELEASE_KEY), as one key signs every real release.
# Writes release-previous/next-version: a version above the previous one
# for this tree's build, the tag's with the patch number one higher and
# the pre-release "e2e" (0.1.0 gives 0.1.1-e2e). The worktree is removed
# afterwards, whatever happens. Needs state.env from up.sh and a clone with
# its tags (fetch-depth: 0 on GitHub).
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

tag=${E2E_PREVIOUS_TAG:-}
if [[ -z $tag ]]; then
	tag=$(git -C "$E2E_ROOT" describe --tags --abbrev=0 HEAD^ 2>/dev/null) ||
		die "no tag before HEAD: fetch the tags (fetch-depth: 0), or set E2E_PREVIOUS_TAG"
fi
git -C "$E2E_ROOT" rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "no tag $tag"
version=${E2E_PREVIOUS_VERSION:-${tag#v}}
[[ $version =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$ ]] ||
	die "the tag $tag doesn't name a version such as v1.2.3: set E2E_PREVIOUS_VERSION"
next=${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] + 1))-e2e

prev=$E2E_WORK/release-previous
tree=$E2E_WORK/tree-previous
remove_tree() {
	git -C "$E2E_ROOT" worktree remove --force "$tree" 2>/dev/null || rm -rf "$tree"
	git -C "$E2E_ROOT" worktree prune
}
remove_tree
rm -rf "$prev"
mkdir -p "$prev"
cp "$E2E_WORK/state.env" "$prev/state.env"

log "the previous release: $tag as $version, in $tree"
trap remove_tree EXIT
git -C "$E2E_ROOT" worktree add -q --detach "$tree" "refs/tags/$tag"
E2E_WORK=$prev E2E_VERSION=$version bash "$tree/e2e/scripts/build.sh"

built=$(python3 -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$prev/dist/manifest.json")
[[ $built == "$version" ]] || die "the previous build's manifest names $built, not $version"
[[ -f $prev/keys/release-key ]] || die "$tag's build.sh left no key at $prev/keys/release-key"
printf '%s\n' "$next" >"$prev/next-version"
log "built $tag as $version in $prev; this tree builds as $next"
