#!/usr/bin/env bash
# Builds what a run needs on this host, which has Go, PHP with Composer, and
# npm (design §14.2):
#   - a throwaway release key, and an e2e release whose base URL is this
#     host on the LXD bridge;
#   - the panel with its dev dependencies, for the contract suite;
#   - the probe client and the root-only test binaries.
# Needs state.env from up.sh, for the bridge address.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

w=$E2E_WORK
rm -rf "${w:?}/keys" "${w:?}/panel-dist" "${w:?}/dist" "${w:?}/contract-test" "${w:?}/bin"
mkdir -p "$w/keys" "$w/bin"
chmod 0700 "$w/keys"

log "a throwaway key and the e2e release $E2E_VERSION for http://$BRIDGE:$E2E_PORT"
(cd "$E2E_ROOT/agent" && go run ./tools/release keygen -out "$w/keys")
# The same two steps as release.yml: the panel first, then the signed release.
bash "$E2E_ROOT/.github/scripts/build-panel.sh" "$E2E_VERSION" "$w/panel-dist"
bash "$E2E_ROOT/.github/scripts/build-release.sh" "$E2E_VERSION" "http://$BRIDGE:$E2E_PORT" "$w/keys/release-key" \
	"$w/panel-dist/zimam-panel-$E2E_VERSION.tar.gz" --e2e
mv "$E2E_ROOT/dist" "$w/dist"

log "the panel with its dev dependencies"
mkdir "$w/contract-test"
git -C "$E2E_ROOT" archive --format=tar HEAD:panel | tar -x -C "$w/contract-test"
(
	cd "$w/contract-test" || exit
	composer install --no-interaction --no-progress --prefer-dist
	cp .env.example .env
	php artisan key:generate
)

log "the probe client and the root-only tests"
(cd "$E2E_ROOT/e2e/probeclient" && CGO_ENABLED=0 go build -trimpath -o "$w/bin/probeclient" .)
(
	cd "$E2E_ROOT/agent" || exit
	for dir in $(grep -rl --include='*_test.go' '^//go:build roottest' . | xargs -n 1 dirname | sort -u); do
		CGO_ENABLED=0 go test -c -tags roottest -o "$w/bin/roottest-$(basename "$dir").test" "$dir"
	done
)
ls -l "$w/dist" "$w/bin"
