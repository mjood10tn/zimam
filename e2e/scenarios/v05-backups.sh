#!/usr/bin/env bash
# v0.5.0's encrypted backups and notifications, through the socket as zimam
# (probeclient) and the panel's own scheduler and queue, against the sinks
# (e2e/sinks: S3 through `rclone serve s3`, SFTP, SMTP). LXD only. On the
# Laravel site "bk" with a database of 200,000 rows and a marker file. In order:
#   1. an S3 destination and an SFTP destination, each tested; the SFTP test
#      pins the sink's host key; the keys read back are the ones sent;
#   2. a plan with bk's files and database and the panel's database runs:
#      three parts, a manifest, one backup listed;
#   3. nothing on the S3 sink is readable: no file or folder name of the
#      site, no SQL keyword, no row text, no SQLite header;
#   4. the database restores into a new database with the same row count,
#      and the files into ~/restores/<backup>/ with the marker's hash;
#   5. a run whose mysqldump is killed fails, leaves no manifest and no new
#      backup, and its run record says why;
#   6. with keep_last 2, two more runs leave two backups;
#   7. the plan's timer runs it (a daily time two minutes ahead), audited
#      as cli:root;
#   8. the SFTP plan runs and lists one backup;
#   9. the panel mails a monitoring warning through the SMTP sink when a
#      test disk passes 85%, and again when it clears.
# Plans, the site and its databases go at the end; the destinations stay
# for probes/backups.sh.
# shellcheck source=../probes/lib.sh
source "$(dirname "$0")/../probes/lib.sh"
load_state

[[ $E2E_TARGET == lxd ]] || {
	echo "SKIP  v05-backups needs the LXD sinks (e2e/sinks); on a VPS it doesn't run (e2e/README.md)"
	exit 0
}
# shellcheck source=/dev/null
source "$E2E_WORK/sinks/sinks.env"
sinks=$E2E_ROOT/e2e/sinks/sinks.sh

site=bk
user=s_$site
home=/home/$user
db=s_bk_main
restored=s_bk_restored
plan=bk-s3
dir=$E2E_WORK/v05
mkdir -p "$dir"
chmod 0700 "$dir"

jq_py() {
	python3 -I -c 'import json, sys; d = json.load(sys.stdin); print(eval(sys.argv[1]))' "$@"
}

data() {
	local reply
	reply=$(request "$1" "$2")
	python3 -I -c 'import json, sys; r = json.load(sys.stdin); assert r["type"] == "result", r; print(json.dumps(r["data"]))' <<<"$reply" ||
		die "FAIL $1 answered: ${reply:0:300}"
}

# job <command> <params> [seconds] runs a job, dies unless it succeeds, and prints its result.
job() {
	local id status
	id=$(submit "$1" "$2") || die "FAIL $1 wasn't accepted"
	status=$(job_end "$id" "${3:-900}")
	[[ $status == succeeded* ]] || die "FAIL $1 ended $status"
	request job.status "{\"job\":\"$id\"}" | jq_py 'json.dumps(d["data"]["result"])'
}

until_true() {
	local end=$((SECONDS + $1))
	shift
	until "$@"; do
		((SECONDS < end)) || return 1
		sleep 3
	done
}

mysql_q() {
	in_c mysql --protocol=socket -N -B -e "$1"
}

backups() {
	data backup.list "{\"plan\":\"$1\"}" | jq_py '" ".join(b["backup"] for b in d["backups"])'
}

secret() {
	python3 -I -c 'import secrets; print(secrets.token_urlsafe(32))'
}

lxc file push -q --mode 0755 "$E2E_WORK/bin/probeclient" "$E2E_CONTAINER$PROBECLIENT"

# The site, its database and a marker file. MySQL comes from v03 in CI.
if [[ $(data system.status '{}' | jq_py '(d.get("services") or {}).get("mysql", {}).get("installed")') != True ]]; then
	job mysql.install '{}' >/dev/null
fi
php=$(data system.status '{}' | jq_py 'd["php"]["default"]')
job site.provision "{\"name\":\"$site\",\"domain\":\"$site.$E2E_DOMAIN\",\"type\":\"laravel\",\"php_version\":\"$php\"}" >/dev/null
job db.create "{\"site\":\"$site\",\"database\":\"$db\"}" >/dev/null
mysql_q "CREATE TABLE $db.items (id INT PRIMARY KEY, note VARCHAR(200));
SET SESSION cte_max_recursion_depth = 300000;
INSERT INTO $db.items WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 200000)
SELECT i, CONCAT('ZQROWMARK-', i, '-', REPEAT('x', 120)) FROM n;"
rows=$(mysql_q "SELECT COUNT(*) FROM $db.items")
[[ $rows == 200000 ]] || die "FAIL the table has $rows rows"
marker=ZQFILEMARK$RANDOM$RANDOM
in_c runuser -u "$user" -- bash -c "mkdir -p $home/site/shared/storage/app && printf '%s\n' $marker >$home/site/shared/storage/app/e2e-marker.txt"
marker_sum=$(in_c sha256sum "$home/site/shared/storage/app/e2e-marker.txt" | cut -d' ' -f1)
pass "site $site with $db ($rows rows) and a marker file in shared/storage/app"

# 1. The destinations.
password=$(secret | cut -c1-43)
salt=$(secret | cut -c1-43)
s3="{\"endpoint\":\"$S3_ENDPOINT\",\"region\":\"us-east-1\",\"bucket\":\"$S3_BUCKET\",\"prefix\":\"servers/e2e\",\"access_key_id\":\"$S3_ACCESS\",\"secret_access_key\":\"$S3_SECRET\"}"
job backup.destination "{\"id\":\"s3\",\"type\":\"s3\",\"s3\":$s3,\"encryption_password\":\"$password\",\"encryption_salt\":\"$salt\"}" >/dev/null
job backup.destination.test '{"id":"s3"}' >/dev/null
keys=$(data backup.destination.keys '{"id":"s3"}' | jq_py 'd["encryption_password"] + " " + d["encryption_salt"]')
[[ $keys == "$password $salt" ]] || die "FAIL backup.destination.keys read back other keys"
printf '%s\n%s\n%s\n%s\n' "$password" "$salt" "$S3_ACCESS" "$S3_SECRET" >"$dir/secrets"
pass "the S3 destination saved and tested; its keys read back as sent"

spassword=$(secret | cut -c1-43)
ssalt=$(secret | cut -c1-43)
printf '%s\n%s\n' "$spassword" "$ssalt" >>"$dir/secrets"
pub=$(job backup.destination "{\"id\":\"sftp\",\"type\":\"sftp\",\"sftp\":{\"host\":\"$SINK_ADDRESS\",\"port\":22,\"user\":\"sftpbackup\",\"path\":\"zimam\"},\"encryption_password\":\"$spassword\",\"encryption_salt\":\"$ssalt\"}" | jq_py 'd["public_key"]')
[[ $pub == ssh-ed25519\ * ]] || die "FAIL the SFTP destination's public key: $pub"
bash "$sinks" authorize "$pub"
fingerprint=$(job backup.destination.test '{"id":"sftp"}' | jq_py 'd["host_key_fingerprint"]')
want=$(lxc exec "$E2E_CONTAINER-sinks" -- ssh-keygen -l -f /etc/ssh/ssh_host_ed25519_key.pub | cut -d' ' -f2)
[[ $fingerprint == "$want" ]] || die "FAIL the SFTP test pinned $fingerprint, the sink's ed25519 key is $want"
pass "the SFTP destination: key pair made, test passed, host key $fingerprint pinned"

# 2. A plan with the site's files, its database and the panel's database.
plan_json() {
	echo "{\"id\":\"$plan\",\"destination\":\"s3\",\"sites\":[\"$site\"],\"databases\":[\"$db\"],\"panel\":true,\"schedule\":\"$1\",\"retention\":{\"keep_last\":2}}"
}
job backup.plan "$(plan_json 'weekly sun 03:00')" >/dev/null
first=$(job backup.run "{\"plan\":\"$plan\"}" 21600 | jq_py 'd["backup"] + " " + ",".join(sorted(p["part"] for p in d["parts"]))')
[[ $first == *" db-$db,files-$site,panel" ]] || die "FAIL the run's parts: $first"
first=${first%% *}
[[ $(backups "$plan") == "$first" ]] || die "FAIL backup.list: $(backups "$plan")"
record=$(data backup.runs "{\"plan\":\"$plan\"}" | jq_py '" ".join(r["status"] + ":" + r["trigger"] for r in d["runs"] if r["backup"] == sys.argv[2])' "$first")
[[ $record == "succeeded:panel" ]] || die "FAIL the run record says $record"
pass "plan $plan ran: backup $first with db-$db, files-$site and panel; listed; recorded succeeded"

# 3. Nothing readable on the sink.
files=$(bash "$sinks" s3-files)
[[ $(wc -l <<<"$files") -ge 4 ]] || die "FAIL the S3 sink holds: $files"
for text in "$marker" ZQROWMARK "INSERT INTO" "CREATE TABLE" "Dump completed" "SQLite format 3" e2e-marker storage "$plan" "$first" manifest; do
	found=$(bash "$sinks" s3-grep "$text")
	[[ -z $found ]] || die "FAIL the S3 sink has '$text' in: $found"
	! grep -qF -- "$text" <<<"$files" || die "FAIL an S3 object's name holds '$text': $files"
done
pass "the S3 sink's $(wc -l <<<"$files") objects show no name, row, SQL or SQLite text"

# 4. Restores.
job backup.restore "{\"plan\":\"$plan\",\"backup\":\"$first\",\"part\":\"db-$db\",\"site\":\"$site\",\"database\":\"$restored\"}" >/dev/null
got=$(mysql_q "SELECT COUNT(*) FROM $restored.items")
[[ $got == "$rows" ]] || die "FAIL the restored database has $got rows, want $rows"
pass "db-$db restored into $restored: $got rows"
folder=$(job backup.restore "{\"plan\":\"$plan\",\"backup\":\"$first\",\"part\":\"files-$site\",\"site\":\"$site\"}" | jq_py 'd["folder"]')
[[ $folder == "$home/restores/$first"* ]] || die "FAIL the files went to $folder"
copy=$(in_c find "$folder" -name e2e-marker.txt)
[[ -n $copy && $(in_c sha256sum "$copy" | cut -d' ' -f1) == "$marker_sum" ]] || die "FAIL the restored marker: '$copy'"
[[ $(in_c stat -c %U "$copy") == "$user" ]] || die "FAIL the restored marker belongs to $(in_c stat -c %U "$copy")"
pass "files-$site restored into $folder, the marker's hash the same, owned by $user"

# 5. A killed mysqldump fails the run and leaves no backup.
# shellcheck disable=SC2016 # expanded by the container's bash
in_c bash -c 'for _ in $(seq 1200); do pkill -KILL -x mysqldump && exit 0; sleep 0.05; done; exit 1' &
killer=$!
id=$(submit backup.run "{\"plan\":\"$plan\"}") || die "FAIL backup.run wasn't accepted"
status=$(job_end "$id" 900)
wait "$killer" || die "FAIL the test never saw mysqldump to kill it"
[[ $status == failed* ]] || die "FAIL the run with a killed mysqldump ended $status"
[[ $(backups "$plan") == "$first" ]] || die "FAIL after the killed run backup.list says $(backups "$plan")"
last=$(data backup.runs "{\"plan\":\"$plan\"}" | jq_py 'd["runs"][-1]["status"] + ": " + d["runs"][-1].get("error", "")')
[[ $last == failed:* ]] || die "FAIL the killed run's record: $last"
pass "a run whose mysqldump was killed failed ($status), left no backup; record: ${last:0:160}"

# 6. Retention keeps the last 2.
for _ in 1 2; do
	sleep 1
	job backup.run "{\"plan\":\"$plan\"}" 21600 >/dev/null
done
kept=$(backups "$plan")
[[ $(wc -w <<<"$kept") == 2 && $kept != *"$first"* ]] || die "FAIL keep_last 2 after three backups: $kept"
pass "keep_last 2: $kept"

# 7. The timer: a daily time two minutes ahead, in the server's zone.
at=$(in_c date -d '+2 minutes' +%H:%M)
job backup.plan "$(plan_json "daily $at")" >/dev/null
in_c systemctl is-active --quiet "zimam-backup-$plan.timer" || die "FAIL zimam-backup-$plan.timer isn't active"
# shellcheck disable=SC2329 # called through until_true
timer_ran() {
	[[ $(data backup.runs "{\"plan\":\"$plan\"}" | jq_py '" ".join(r["status"] for r in d["runs"] if r["trigger"] == "timer")') == *succeeded* ]]
}
until_true 600 timer_ran || die "FAIL no timer run of $plan within 10 minutes: $(in_c journalctl -u "zimam-backup-$plan" -n 20 --no-pager)"
in_c grep -q '"actor":"cli:root","command":"backup run"' /var/log/zimam/audit.log || die "FAIL the timer run isn't audited as cli:root"
pass "the timer ran $plan at $at (plus its random delay), audited as cli:root; kept: $(backups "$plan")"

# 8. The SFTP plan.
job backup.plan "{\"id\":\"bk-sftp\",\"destination\":\"sftp\",\"sites\":[\"$site\"],\"schedule\":\"weekly sun 04:00\",\"retention\":{\"keep_last\":1}}" >/dev/null
job backup.run '{"plan":"bk-sftp"}' 21600 >/dev/null
[[ $(wc -w <<<"$(backups bk-sftp)") == 1 ]] || die "FAIL bk-sftp lists: $(backups bk-sftp)"
pass "plan bk-sftp ran over SFTP and lists one backup"

# 9. A monitoring warning reaches the SMTP sink, and its clear.
fillmount=/mnt/e2e-fill
mkdir -p "$E2E_WORK/fill"
as_root truncate -s 64M "$E2E_WORK/fill.img"
as_root mkfs.ext4 -q -F "$E2E_WORK/fill.img"
as_root mount -o loop "$E2E_WORK/fill.img" "$E2E_WORK/fill"
lxc config device add "$E2E_CONTAINER" e2e-fill disk source="$E2E_WORK/fill" path="$fillmount" >/dev/null
# shellcheck disable=SC2016 # PHP's variables
setup='require "vendor/autoload.php"; $app = require "bootstrap/app.php"; $app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
app(App\Settings\PanelSettings::class)->setSmtp(["host" => $argv[1], "port" => 2525, "security" => "starttls", "username" => "", "password" => "", "from" => "zimam@zimam.test"]);
App\Models\NotificationChannel::create(["type" => "email", "name" => "e2e", "settings" => ["to" => "owner@zimam.test"], "events" => ["monitoring"], "enabled" => true]);'
lxc exec "$E2E_CONTAINER" --cwd /home/zimam/panel/current --env HOME=/home/zimam -- runuser -u zimam -- "$(panel_php)" -r "$setup" -- "$SINK_ADDRESS"
as_root dd if=/dev/zero of="$E2E_WORK/fill/full" bs=1M count=52 status=none
# shellcheck disable=SC2329 # called through until_true
mailed() {
	# The messages are quoted-printable: a soft line break ("=" at the end of
	# a line) can fall inside the path, so they are decoded before the search.
	bash "$sinks" mail | python3 -I -c 'import quopri, sys; sys.stdout.buffer.write(quopri.decodestring(sys.stdin.buffer.read()))' | grep -q -- "$1"
}
until_true 300 mailed "$fillmount" || die "FAIL no warning mail for $fillmount within 5 minutes: $(bash "$sinks" mail | tail -n 20)"
pass "the warning for $fillmount reached the SMTP sink"
as_root rm "$E2E_WORK/fill/full"
until_true 300 mailed "Cleared: disk:$fillmount" || die "FAIL no clear mail within 5 minutes: $(bash "$sinks" mail | tail -n 20)"
pass "its clear reached the SMTP sink too"
lxc config device remove "$E2E_CONTAINER" e2e-fill >/dev/null
as_root umount "$E2E_WORK/fill"
as_root rm -f "$E2E_WORK/fill.img"

# The plans and the site go; the destinations stay for probes/backups.sh.
job backup.plan.delete "{\"plan\":\"$plan\"}" >/dev/null
job backup.plan.delete '{"plan":"bk-sftp"}' >/dev/null
! in_c test -e "/etc/systemd/system/zimam-backup-$plan.timer" || die "FAIL the timer unit is still there"
job site.destroy "{\"name\":\"$site\",\"drop_databases\":true}" >/dev/null
pass "the plans (units gone) and the site with its databases deleted"

exit "$failed"
