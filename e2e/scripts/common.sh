# shellcheck shell=bash disable=SC2034 # the scripts that source this use its settings
# Sourced by the end-to-end scripts: names, the work directory and the
# helpers they share. Settings come from the environment, so the same
# scripts run on a GitHub runner and on a local VM that hosts LXD
# (README.md), and against a real server over SSH (E2E_TARGET=vps,
# e2e/vps/README.md).
set -Eeuo pipefail

E2E_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_WORK=${E2E_WORK:-/tmp/zimam-e2e}
E2E_CONTAINER=${E2E_CONTAINER:-zimam-e2e}
E2E_IMAGE=${E2E_IMAGE:-ubuntu:26.04}
E2E_VERSION=${E2E_VERSION:-0.0.0-e2e}
E2E_PORT=8080
# lxd: an LXD container on this host, with Pebble. vps: a real server that
# e2e/vps/create.sh made, reached over SSH (E2E_SSH, its user@address),
# with Let's Encrypt staging and the server's firewall on.
E2E_TARGET=${E2E_TARGET:-lxd}
# The domain every test name sits under: challtestsrv answers zimam.test
# with the container; on a VPS, sslip.io answers <ip-with-dashes>.sslip.io.
E2E_DOMAIN=${E2E_DOMAIN:-zimam.test}
E2E_PANEL=panel.$E2E_DOMAIN
E2E_NAMES=("$E2E_PANEL" "static.$E2E_DOMAIN" "php84.$E2E_DOMAIN" "php85.$E2E_DOMAIN")
# A setup link, as the installer and `panel setup-link` print it.
E2E_SETUP_LINK="^https://${E2E_PANEL//./\\.}/setup/[A-Za-z0-9_-]{43}\$"
# The root the test clients trust, and how the installer reaches the ACME
# server and the firewall: browser.sh fetches this Pebble run's root; on a
# VPS, create.sh writes Let's Encrypt's staging roots, pinned in vps.yml.
if [[ $E2E_TARGET == vps ]]; then
	E2E_CA=$E2E_WORK/staging-roots.pem
	E2E_INSTALL_FLAGS="--acme-server https://acme-staging-v02.api.letsencrypt.org/directory"
else
	E2E_CA=$E2E_WORK/pebble-root.pem
	E2E_INSTALL_FLAGS="--acme-server https://pebble:14000/dir --acme-ca-bundle /root/pebble-ca.pem --skip-firewall"
fi

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

# lxc finds the snap's lxc even before /snap/bin is on PATH. On a VPS,
# e2e/vps/lxc-ssh stands in for it: its exec and file push go over SSH.
lxc() {
	if [[ $E2E_TARGET == vps ]]; then
		E2E_WORK=$E2E_WORK "$E2E_ROOT/e2e/vps/lxc-ssh" "$@"
	else
		as_root "$(type -P lxc || echo /snap/bin/lxc)" "$@"
	fi
}

# in_c runs a command in the container as root.
in_c() {
	lxc exec "$E2E_CONTAINER" -- "$@"
}

# load_state reads what up.sh found: BRIDGE (this host on the LXD bridge,
# where the container gets the release) and CIP (the container's address).
# On a VPS, create.sh writes them: BRIDGE is 127.0.0.1, since the release is
# served on the server itself; CIP and CIP6 are its public addresses, and
# CLIENT is this host's address as the server sees it. CLIENT is BRIDGE in
# LXD.
load_state() {
	[[ -f $E2E_WORK/state.env ]] || die "no $E2E_WORK/state.env: run e2e/scripts/up.sh first"
	# shellcheck source=/dev/null
	source "$E2E_WORK/state.env"
	CLIENT=${CLIENT:-$BRIDGE}
}

# The panel's PHP binary, from its queue unit: plain php is the server's
# default, which milestone 2 changes.
panel_php() {
	in_c bash -c "systemctl cat zimam-panel-queue.service | grep -o '/usr/bin/php[0-9.]*' | head -n 1"
}

# artisan runs the panel's artisan as zimam.
artisan() {
	local php
	php=$(panel_php)
	lxc exec "$E2E_CONTAINER" --cwd /home/zimam/panel/current --env HOME=/home/zimam -- runuser -u zimam -- "$php" artisan "$@"
}

# panel_sql <sql> [values...] prints the query's rows as one JSON array,
# read as zimam from the panel's database; the values bind its ?s.
panel_sql() {
	local php
	php=$(panel_php)
	# shellcheck disable=SC2016 # PHP's variables
	lxc exec "$E2E_CONTAINER" --env HOME=/home/zimam -- runuser -u zimam -- "$php" -r '
		$db = new PDO("sqlite:/home/zimam/panel/shared/database.sqlite", null, null, [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION]);
		$q = $db->prepare($argv[1]);
		$q->execute(array_slice($argv, 2));
		echo json_encode($q->fetchAll(PDO::FETCH_ASSOC)), "\n";' -- "$@"
}

# jobs_mark prints the newest agent_jobs row's id, so wait_jobs can wait
# for the jobs submitted after it.
jobs_mark() {
	panel_sql 'select coalesce(max(id), 0) as id from agent_jobs' | python3 -I -c 'import json, sys; print(json.load(sys.stdin)[0]["id"])'
}

# wait_jobs <mark> <command> <count> <status> waits up to 5 minutes for
# <count> <command> jobs submitted after <mark> to finish, and fails unless
# every one ended <status>.
wait_jobs() {
	local mark=$1 command=$2 count=$3 want=$4 rows
	for _ in $(seq 1 150); do
		rows=$(panel_sql 'select id, status, error_message from agent_jobs where id > ? and command = ? order by id' "$mark" "$command")
		if python3 -I - "$count" "$want" "$command" "$rows" <<'PY'
import json, sys

count, want, command = int(sys.argv[1]), sys.argv[2], sys.argv[3]
rows = json.loads(sys.argv[4])
done = [r for r in rows if r["status"] in ("succeeded", "failed", "cancelled")]
if len(rows) < count or len(done) < len(rows):
    sys.exit(2)
bad = [r for r in rows if r["status"] != want]
if bad:
    print(f"e2e: {command} jobs that didn't end {want}: {bad}", file=sys.stderr)
    sys.exit(1)
sys.exit(0)
PY
		then
			return 0
		else
			(($? == 1)) && return 1
		fi
		sleep 2
	done
	die "$count $command jobs after $mark didn't finish within 5 minutes: $rows"
}
