# shellcheck shell=bash disable=SC2034 # the scripts that source this use its settings
# Sourced by the end-to-end scripts: names, the work directory and the
# helpers they share. Settings come from the environment, so the same
# scripts run on a GitHub runner and on a local VM that hosts LXD
# (README.md).
set -Eeuo pipefail

E2E_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_WORK=${E2E_WORK:-/tmp/zimam-e2e}
E2E_CONTAINER=${E2E_CONTAINER:-zimam-e2e}
E2E_IMAGE=${E2E_IMAGE:-ubuntu:26.04}
E2E_VERSION=${E2E_VERSION:-0.0.0-e2e}
E2E_PORT=8080
E2E_PANEL=panel.zimam.test
# The names the milestones use; challtestsrv answers them with the container.
E2E_NAMES=(panel.zimam.test static.zimam.test php84.zimam.test php85.zimam.test)

log() { printf '== %s\n' "$*" >&2; }
die() {
	printf 'e2e: %s\n' "$*" >&2
	exit 1
}

# as_root runs a command as root. A GitHub runner's user has sudo.
as_root() {
	if ((EUID == 0)); then
		"$@"
	else
		sudo "$@"
	fi
}

# lxc finds the snap's lxc even before /snap/bin is on PATH.
lxc() {
	as_root "$(type -P lxc || echo /snap/bin/lxc)" "$@"
}

# in_c runs a command in the container as root.
in_c() {
	lxc exec "$E2E_CONTAINER" -- "$@"
}

# load_state reads what up.sh found: BRIDGE (this host on the LXD bridge)
# and CIP (the container's address).
load_state() {
	[[ -f $E2E_WORK/state.env ]] || die "no $E2E_WORK/state.env: run e2e/scripts/up.sh first"
	# shellcheck source=/dev/null
	source "$E2E_WORK/state.env"
}
