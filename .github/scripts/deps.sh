#!/usr/bin/env bash
# Checks every dependency set for known vulnerabilities (design §14, R01-D12).
# Stops at the first finding. ci.yml and nightly.yml run it, so a new advisory
# fails the nightly run without a code change. It needs go, composer and npm.
#
# govulncheck runs through `go run` at a pinned version and never enters
# agent/go.mod. Version v1.9.0 was chosen on 2026-11-15; bump it on purpose.
#
# Usage: deps.sh
set -Eeuo pipefail

govulncheck="golang.org/x/vuln/cmd/govulncheck@v1.9.0"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

step() {
	echo "== $*"
}

step "govulncheck: agent"
(cd "$root/agent" && go run "$govulncheck" ./...)

step "govulncheck: e2e/probeclient"
(cd "$root/e2e/probeclient" && go run "$govulncheck" ./...)

step "composer audit: panel"
(cd "$root/panel" && composer audit --locked --no-interaction)

step "npm audit: panel (production)"
(cd "$root/panel" && npm audit --omit=dev --audit-level=high)

step "npm audit: e2e/browser"
(cd "$root/e2e/browser" && npm audit --audit-level=high)

echo "deps: no known vulnerabilities at the chosen levels"
