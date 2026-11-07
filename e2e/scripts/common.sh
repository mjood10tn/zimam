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
