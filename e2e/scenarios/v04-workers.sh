#!/usr/bin/env bash
# v0.4.0's workers, scheduler, cron jobs and Node.js, through the socket as
# zimam (probeclient), on the Laravel site "workers", which deploys the Git
# host's zimam-e2e/laravel-app (e2e/githost) and uses Valkey through its own
# user. It runs after v03-databases, which installed Valkey, on the PHP
# versions milestone 2 installed (8.4 and 8.5), and leaves Node.js 26
# installed for the browser's v04-workers spec. In order:
#   1. the installer's cron runs, and /etc/cron.allow and /etc/at.allow
#      admit root only;
#   2. node.install 24, a change of major to 26, node.uninstall, and
#      node.install of the default (26) again; system.status follows each;
#   3. the site deploys with `npm ci && npm run build` in its script: the
#      fixture's Vite-shaped build writes public/build with Node 26;
#   4. a laravel-queue worker with 2 processes runs as the site user, in its
#      sandbox; a queued job leaves its file; site.workers.status,
#      system.metrics and the worker journal (logs.read) see it;
#   5. the scheduler is turned on and a */1 cron job added: the scheduled
#      command's file appears within two minutes and the job's line reaches
#      logs/cron.log, which logs.read reads; the cron file names only the
#      site's user;
#   6. a second deploy restarts both processes, and a job runs on the new
#      release; site.php to 8.5 restarts them again, and a job (and the
#      scheduler) runs on PHP 8.5; site.workers.restart restarts one worker;
#   7. the site is destroyed: no unit, target, script, cron file, process or
#      home is left.
# shellcheck source=../probes/lib.sh
source "$(dirname "$0")/../probes/lib.sh"
load_state

site=workers
user=s_$site
home=/home/$user
host=$site.$E2E_DOMAIN
dir=$E2E_WORK/v04
mkdir -p "$dir"
githost=$E2E_ROOT/e2e/githost/githost.sh
units=/etc/systemd/system
worker=queue
instances=("zimam-worker-${site}_$worker@1.service" "zimam-worker-${site}_$worker@2.service")

# json prints its argument as a JSON string.
json() {
	python3 -I -c 'import json, sys; print(json.dumps(sys.argv[1]))' "$1"
}

# data <command> <params> prints a read's data as JSON, or fails with the reply.
data() {
	local reply
	reply=$(request "$1" "$2")
	python3 -I -c 'import json, sys; r = json.load(sys.stdin); assert r["type"] == "result", r; print(json.dumps(r["data"]))' <<<"$reply" ||
		die "FAIL $1 answered: ${reply:0:300}"
}

# jq_py <expression> evaluates a Python expression over the JSON on stdin, as d.
jq_py() {
	python3 -I -c 'import json, sys; d = json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"
}

# must <what> <command...> fails the scenario when the command fails.
must() {
	local what=$1
	shift
	"$@" || die "FAIL $what"
}

# job <command> <params> runs a job and dies unless it succeeds.
job() {
	local id status
	id=$(submit "$1" "$2") || die "FAIL $1 wasn't accepted"
	status=$(job_end "$id" 600)
	[[ $status == succeeded* ]] || die "FAIL $1 ended $status"
}

# until_true <seconds> <command...> retries the command every 2 seconds.
until_true() {
	local end=$((SECONDS + $1))
	shift
	until "$@"; do
		((SECONDS < end)) || return 1
		sleep 2
	done
}

get() {
	in_c curl -fsS -m 20 --resolve "$host:80:127.0.0.1" "http://$host/${1:-}"
}

live() {
	in_c readlink -f "$home/site/current"
}

# pids prints each worker process's main PID, one line, "-" for none.
pids() {
	local unit pid list=()
	for unit in "${instances[@]}"; do
		pid=$(in_c systemctl show -p MainPID --value "$unit")
		[[ $pid != 0 ]] || pid=-
		list+=("$pid")
	done
	echo "${list[*]}"
}

# running says whether both processes run as the site user.
# shellcheck disable=SC2329 # called through until_true
running() {
	local unit
	for unit in "${instances[@]}"; do
		[[ $(in_c systemctl show -p ActiveState,SubState,User --value "$unit" | paste -sd ' ') == "active running $user" ]] || return 1
	done
}

# restarted <old pids> waits until both processes run with other PIDs.
# shellcheck disable=SC2329 # called through must
restarted() {
	local old=$1 now
	# shellcheck disable=SC2329 # called through until_true
	moved() {
		now=$(pids)
		running || return 1
		local a b
		read -r -a a <<<"$old"
		read -r -a b <<<"$now"
		[[ ${a[0]} != "${b[0]}" && ${a[1]} != "${b[1]}" ]]
	}
	until_true 120 moved
}

# queued <word> queues a job through the app and prints the file it leaves
# once a worker has run it.
queued() {
	local word=$1 file=$home/e2e/job-$1
	[[ $(get "?queue=$word") == "queued=$word" ]] || die "FAIL the app didn't queue $word"
	until_true 60 in_c test -s "$file" || die "FAIL no worker ran the job $word: $(in_c journalctl -u "zimam-worker-${site}_*" -n 20 --no-pager)"
	in_c cat "$file"
}

state_workers() {
	in_c cat "/var/lib/zimam/sites/$site.json" | jq_py 'len(d.get("workers") or [])'
}

lxc file push -q --mode 0755 "$E2E_WORK/bin/probeclient" "$E2E_CONTAINER$PROBECLIENT"

# 1. cron, and who may use crontab and at.
must "cron isn't running" in_c systemctl is-active --quiet cron
must "cron isn't enabled" in_c systemctl is-enabled --quiet cron
for allow in /etc/cron.allow /etc/at.allow; do
	[[ $(in_c stat -c '%U %a' "$allow") == "root 644" && $(in_c cat "$allow") == root ]] ||
		die "FAIL $allow is $(in_c stat -c '%U %a' "$allow" 2>&1): $(in_c cat "$allow" 2>&1)"
done
pass "cron runs and is enabled; /etc/cron.allow and /etc/at.allow are root's (0644) and name only root"

# 2. Node.js: 24, a change of major to 26, uninstall, and the default again.
node_state() {
	data system.status '{}' | jq_py '"installed=%s major=%s version=%s" % ((d.get("node") or {}).get("installed"), (d.get("node") or {}).get("major", ""), (d.get("node") or {}).get("version", ""))'
}
for major in 24 26; do
	job node.install "{\"major\":\"$major\"}"
	state=$(node_state)
	version=$(in_c node --version)
	# system.status names the nodejs package's version, such as 24.22.3-1nodesource1.
	[[ $state == "installed=True major=$major version=${version#v}"* && $version == "v$major."* ]] ||
		die "FAIL after node.install $major: system.status says $state, node --version $version"
	in_c grep -q "node_$major.x" /etc/apt/sources.list.d/zimam-nodesource.sources || die "FAIL the NodeSource source doesn't follow $major.x"
	pass "node.install $major: $version, npm $(in_c npm --version); system.status: $state"
done
job node.uninstall '{}'
state=$(node_state)
[[ $state == "installed=False major= version=" ]] || die "FAIL after node.uninstall, system.status says $state"
! in_c test -e /usr/bin/node || die "FAIL /usr/bin/node is still there"
! in_c test -e /etc/apt/sources.list.d/zimam-nodesource.sources || die "FAIL the NodeSource source is still there"
pass "node.uninstall: no node, no NodeSource source; system.status: $state"
probe "node.uninstall when Node.js isn't installed" not_found "{$env_ok,\"command\":\"node.uninstall\",\"params\":{}}"
job node.install '{}'
state=$(node_state)
[[ $state == "installed=True major=26 "* ]] || die "FAIL node.install of the default: $state"
pass "node.install of the default: $state"

# 3. The site, its Valkey user and .env, and a deploy that builds with npm.
job site.provision "{\"name\":\"$site\",\"domain\":\"$host\",\"type\":\"laravel\",\"php_version\":\"8.4\"}"
python3 -I -c 'import secrets; print(secrets.token_urlsafe(32))' >"$dir/valkey.pw"
chmod 600 "$dir/valkey.pw"
job cache.user "{\"site\":\"$site\",\"password\":\"$(cat "$dir/valkey.pw")\"}"
env="APP_ENV=production
E2E_GREETING=workers
QUEUE_CONNECTION=redis
CACHE_STORE=redis
REDIS_CLIENT=phpredis
REDIS_HOST=127.0.0.1
REDIS_PORT=6379
REDIS_USERNAME=$user
REDIS_PASSWORD=$(cat "$dir/valkey.pw")
REDIS_PREFIX=$user:
"
job site.env.write "{\"name\":\"$site\",\"content\":$(json "$env")}"
id=$(submit site.repo "{\"name\":\"$site\",\"repository\":\"zimam-e2e/laravel-app\",\"branch\":\"main\"}") || die "FAIL site.repo wasn't accepted"
status=$(job_end "$id")
[[ $status == succeeded* ]] || die "FAIL site.repo ended $status"
key=$(request job.status "{\"job\":\"$id\"}" | jq_py 'd["data"]["result"]["public_key"]')
bash "$githost" authorize "$key"
script='php artisan config:cache && npm ci && npm run build'
deploy="{\"name\":\"$site\",\"composer\":true,\"trigger\":\"manual\",\"script\":$(json "$script")}"
job site.deploy "$deploy"
first=$(live)
built=$(in_c runuser -u "$user" -- cat "$first/public/build/node.txt")
manifest=$(in_c runuser -u "$user" -- cat "$first/public/build/manifest.json" | jq_py 'd["resources/js/app.js"]["file"]')
[[ $built == v26.* && $manifest == assets/app-*.js ]] || die "FAIL the deploy's npm build: node.txt $built, manifest $manifest"
in_c runuser -u "$user" -- test -f "$first/public/build/$manifest" || die "FAIL the manifest's $manifest wasn't built"
[[ $(get) == "Zimam e2e app release="*" greeting=workers" ]] || die "FAIL the site answers $(get)"
pass "the deploy ran 'npm ci && npm run build' with Node $built: public/build/$manifest and its manifest, in $first"

# 4. A laravel-queue worker with 2 processes on Valkey.
job site.workers "{\"name\":\"$site\",\"workers\":[{\"id\":\"$worker\",\"kind\":\"laravel-queue\",\"processes\":2}]}"
must "the worker's processes aren't running as $user: $(in_c systemctl status --no-pager "zimam-worker-${site}_*" 2>&1 | tail -n 20)" until_true 60 running
for f in "$units/zimam-worker-${site}_$worker@.service" "$units/zimam-site-$site.target"; do
	in_c test -f "$f" || die "FAIL no $f"
done
in_c systemctl is-enabled --quiet "zimam-site-$site.target" || die "FAIL zimam-site-$site.target isn't enabled"
script_mode=$(in_c stat -c '%U %a' "$home/.zimam/workers/$worker.sh")
[[ $script_mode == "$user 700" ]] || die "FAIL the worker's script is $script_mode"
in_c grep -qx "exec 'php' 'artisan' 'queue:work' 'default' '--memory=512'" "$home/.zimam/workers/$worker.sh" ||
	die "FAIL the worker's script runs: $(in_c tail -n 1 "$home/.zimam/workers/$worker.sh")"
[[ $(state_workers) == 1 ]] || die "FAIL the site's state lists $(state_workers) workers"
pass "worker $worker: 2 processes active as $user ($(pids)), its unit and target in $units, its script $user 0700 runs queue:work"
# The sandbox (R04-D2): NoNewPrivileges, a read-only system, other homes hidden.
props=$(in_c systemctl show -p NoNewPrivileges,ProtectSystem,ProtectHome,MemoryMax,User "${instances[0]}" | paste -sd ' ')
[[ $props == *NoNewPrivileges=yes* && $props == *ProtectSystem=strict* && $props == *ProtectHome=tmpfs* && $props == *MemoryMax=536870912* ]] ||
	die "FAIL the worker unit's sandbox: $props"
pass "the worker's unit: $props"
out=$(queued one)
[[ $out == "php=8.4."*" release=$first" ]] || die "FAIL the job ran as: $out, want PHP 8.4 on $first"
pass "a queued job ran in a worker: $out"
status=$(data site.workers.status "{\"name\":\"$site\"}" | jq_py '" ".join("%s@%s:%s/%s" % (w["id"], i["instance"], i["active"], i["sub"]) for w in d["workers"] for i in w["instances"])')
[[ $status == "$worker@1:active/running $worker@2:active/running" ]] || die "FAIL site.workers.status says $status"
pass "site.workers.status: $status"
metric=$(data system.metrics '{}' | jq_py '[(m["site"], m["worker"], m["failing"], m["failed"]) for m in d.get("workers") or []]')
[[ $metric == *"('$site', '$worker', False, False)"* ]] || die "FAIL system.metrics' workers: $metric"
pass "system.metrics lists the worker, not failing: $metric"
journal=$(data logs.read "{\"source\":\"site.workers\",\"name\":\"$site\",\"direction\":\"back\",\"limit\":50}" | jq_py '"\n".join(e.get("message", "") for e in d.get("entries") or [])')
[[ $journal == *"processed one"* ]] || die "FAIL the worker journal (logs.read site.workers) has no 'processed one': ${journal:0:500}"
pass "logs.read site.workers shows the worker's output: processed one"

# 5. The scheduler and a cron job every minute.
# shellcheck disable=SC2016 # the cron job's command, run by the site user's bash
tick='echo "e2e-tick user=$(id -un) php=$(php -r "echo PHP_VERSION;")"'
cron="{\"name\":\"$site\",\"scheduler\":true,\"jobs\":[{\"id\":\"tick\",\"schedule\":\"*/1 * * * *\",\"command\":$(json "$tick"),\"enabled\":true}]}"
job site.cron "$cron"
cronfile=/etc/cron.d/zimam-$site
[[ $(in_c stat -c '%U %a' "$cronfile") == "root 644" ]] || die "FAIL $cronfile is $(in_c stat -c '%U %a' "$cronfile")"
lines=$(in_c grep -Ev '^(#|$|[A-Z]+=)' "$cronfile")
[[ $(wc -l <<<"$lines") == 2 ]] || die "FAIL $cronfile has these entries: $lines"
awk -v u="$user" '$6 != u { bad = 1 } END { exit bad }' <<<"$lines" || die "FAIL $cronfile has an entry for another user: $lines"
pass "$cronfile (root 0644) has the scheduler and tick, both as $user:
$lines"
must "the scheduled command left no ~/e2e/schedule.log within two minutes" until_true 130 in_c test -s "$home/e2e/schedule.log"
pass "the scheduler ran: $(in_c tail -n 1 "$home/e2e/schedule.log")"
must "tick's line didn't reach logs/cron.log within two minutes" until_true 130 in_c grep -q "e2e-tick user=$user php=8.4" "$home/logs/cron.log"
in_c grep -Eq '\[tick\] finished with status 0$' "$home/logs/cron.log" || die "FAIL cron.log has no finished tick: $(in_c tail -n 5 "$home/logs/cron.log")"
pass "tick ran as $user and logged to cron.log: $(in_c grep -m 1 e2e-tick "$home/logs/cron.log")"
cronlog=$(data logs.read "{\"source\":\"site.cron\",\"name\":\"$site\",\"direction\":\"back\",\"limit\":50}" | jq_py '"\n".join(l.get("text", l.get("line", "")) if isinstance(l, dict) else str(l) for l in d.get("lines") or [])')
[[ $cronlog == *"e2e-tick user=$user"* ]] || die "FAIL logs.read site.cron has no tick line: ${cronlog:0:500}"
pass "logs.read site.cron shows the tick"
out=$(in_c runuser -u "$user" -- crontab -l 2>&1) && die "FAIL $user may use crontab: $out"
pass "$user may not use crontab (cron.allow): $out"

# 6. Workers restart on a deploy, on a PHP switch and on site.workers.restart.
before=$(pids)
sleep 1 # release ids are per second
job site.deploy "$deploy"
second=$(live)
[[ $second != "$first" ]] || die "FAIL the second deploy didn't move current"
must "the workers didn't restart after the deploy (were $before, are $(pids))" restarted "$before"
out=$(queued two)
[[ $out == "php=8.4."*" release=$second" ]] || die "FAIL after the deploy the job ran as: $out, want $second"
pass "the deploy restarted both processes ($before -> $(pids)); a job ran on the new release: $out"

before=$(pids)
job site.php "{\"name\":\"$site\",\"php_version\":\"8.5\"}"
must "the workers didn't restart after the PHP switch (were $before, are $(pids))" restarted "$before"
out=$(queued three)
[[ $out == "php=8.5."*" release=$second" ]] || die "FAIL after site.php 8.5 the job ran as: $out"
in_c grep -q '8.5' "$home/.zimam/site.env.sh" || die "FAIL site.env.sh doesn't name 8.5: $(in_c cat "$home/.zimam/site.env.sh")"
pass "site.php 8.5 restarted both processes ($before -> $(pids)); a job ran on PHP 8.5: $out"
must "the scheduler didn't run on PHP 8.5 within two minutes" until_true 130 in_c grep -q ' php=8\.5\.' "$home/e2e/schedule.log"
pass "the scheduler runs on PHP 8.5 too: $(in_c grep -m 1 ' php=8\.5\.' "$home/e2e/schedule.log")"

before=$(pids)
job site.workers.restart "{\"name\":\"$site\",\"worker\":\"$worker\"}"
must "site.workers.restart didn't restart the processes (were $before, are $(pids))" restarted "$before"
pass "site.workers.restart $worker restarted both processes ($before -> $(pids))"

# 7. Destroying the site leaves nothing of its workers and cron jobs.
uid=$(in_c id -u "$user")
job site.destroy "{\"name\":\"$site\",\"drop_databases\":true}"
left=$(in_c find "$units" /etc/cron.d -name "zimam-worker-${site}_*" -o -name "zimam-site-$site.target" -o -name "zimam-$site")
[[ -z $left ]] || die "FAIL the site left: $left"
loaded=$(in_c systemctl list-units --all --plain --no-legend "zimam-worker-${site}_*" "zimam-site-$site.target")
[[ -z $loaded ]] || die "FAIL systemd still has: $loaded"
! in_c test -e "$home" || die "FAIL $home is still there"
procs=$(in_c ps -o pid=,cmd= -u "$uid" || true)
[[ -z $procs ]] || die "FAIL uid $uid still runs: $procs"
pass "site.destroy: no worker unit, target, cron file, home (scripts included) or process of uid $uid is left"

exit "$failed"
