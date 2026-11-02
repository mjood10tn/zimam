#!/usr/bin/env bash
# Running the installer again repairs the server (design §4). The installed
# agent reports its release, the new one passes `version --at-least` for it
# (D25), bootstrap finds its steps done, and the last line is a new setup
# link. Runs last: the new link replaces the one the browser tests used.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

installed=$(in_c /usr/local/sbin/zimam-agent version)
[[ $installed == "$E2E_VERSION" ]] || die "FAIL repair: the installed agent reports $installed, not $E2E_VERSION"
cmd="curl -fsSL http://$BRIDGE:$E2E_PORT/latest/download/install.sh | bash -s -- --panel-domain $E2E_PANEL"
cmd+=" --acme-server https://pebble:14000/dir --acme-ca-bundle /root/pebble-ca.pem --skip-firewall --yes"
log "$cmd"
code=0
in_c bash -c "$cmd" >"$E2E_WORK/repair.out" 2>"$E2E_WORK/repair.err" || code=$?
cat "$E2E_WORK/repair.err" >&2
[[ $code == 0 ]] || die "FAIL repair: the installer exited $code"
last=$(tail -n 1 "$E2E_WORK/repair.out")
[[ $last =~ ^https://panel\.zimam\.test/setup/[A-Za-z0-9_-]{43}$ ]] ||
	die "FAIL repair: the installer's last line isn't a setup link: $last"
grep -q 'panel-release: already done' "$E2E_WORK/repair.err" ||
	die "FAIL repair: bootstrap installed the panel release again"
echo "PASS  repair: the installer ran again, passed the version check and printed a new setup link"
