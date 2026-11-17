#!/usr/bin/env bash
# v0.3.0's MySQL databases and Valkey cache users, outside the browser:
# e2e/browser/databases.spec.ts drives the panel (installing MySQL and
# Valkey, the Laravel sites "alpha" and "beta", a database each, a MySQL and a
# Valkey user each, writing them to .env, deploying the fixture app, rotating
# alpha's passwords, deleting the sites) and calls these steps for what only the
# server can see. The fixture app (e2e/fixtures/laravel-app, app/Services.php)
# writes a row and a cache key as its own users and runs a queue job through
# Valkey. The passwords the browser saw are kept in $E2E_WORK/v03, one file
# per site and service (<site>-mysql.pw, <site>-valkey.pw; the rotated-away
# one gets ".old"), so these steps can log in with them.
#   v03-databases.sh snapshot before|after
#                                 fs-hash of /etc, /root and /home; "after"
#                                 compares it with "before": only what
#                                 installing and using the two services
#                                 changes may differ (the list below)
#   v03-databases.sh mem <label>  free memory on this host, and MySQL's and
#                                 Valkey's resident memory in the server
#   v03-databases.sh mark         remember the newest agent job
#   v03-databases.sh wait <command> <count> <status>
#                                 <count> <command> jobs since the mark ended <status>
#   v03-databases.sh installed    MySQL and Valkey run, listen on 127.0.0.1 only,
#                                 and the default Valkey user is off
#   v03-databases.sh authorize <site>
#                                 the site's deploy key may fetch from the Git host
#   v03-databases.sh env <site>   shared/.env has the DB_ and REDIS_ keys with the
#                                 saved passwords
#   v03-databases.sh serves <site>
#                                 the site answers with the Git host's release
#   v03-databases.sh uses <site> <word>
#                                 the app writes and reads its row, its cache key
#                                 and a queue job; only the site's own database
#                                 and prefix hold them
#   v03-databases.sh stale <site> the app's cached config no longer logs in
#   v03-databases.sh logins <site> new|old
#                                 the site's users log in with the new passwords
#                                 and are refused with the old ones (or the reverse)
#   v03-databases.sh worker <site> start|alive|dead
#                                 a queue worker as the site user
#   v03-databases.sh scratch <site> present|gone
#                                 the site's second database, made on the MySQL page, is
#                                 there with its grants, or dropped with them
#   v03-databases.sh gone <site>  the site's MySQL users and databases, Valkey user
#                                 and keys, and its home are gone
#   v03-databases.sh kept <site>  all of them are still there
# shellcheck source=../scripts/services.sh
source "$(dirname "$0")/../scripts/services.sh"
load_state

dir=$E2E_WORK/v03
mkdir -p "$dir"
githost=$E2E_ROOT/e2e/githost/githost.sh
fshash=/usr/local/bin/zimam-fs-hash

pass() { echo "PASS  $*"; }
fail() { die "FAIL $*"; }

get() {
	local site=$1
	shift
	in_c curl -sS -m 20 -w '\n%{http_code}' --resolve "$site.$E2E_DOMAIN:80:127.0.0.1" "http://$site.$E2E_DOMAIN/$*"
}

# body_of and code_of split get's answer: the body, and the status code.
body_of() { sed '$d' <<<"$1"; }
code_of() { tail -n 1 <<<"$1"; }

user_of() { echo "s_$1"; }
database_of() { echo "s_$1_app"; }
pw() { echo "$dir/$1-$2.pw${3:+.$3}"; }

# The changes installing MySQL and Valkey and using them for two sites that
# are then deleted may leave under /etc, /root and /home. Every other path
# must be what it was before: a site's own files and the probes' leftovers
# are not on this list.
allowed_changes() {
	cat <<'EOF'
# MySQL's package: its configuration, the package's own files and the agent's
# zz-zimam.cnf (/etc/mysql), the alternative that points my.cnf at them,
# AppArmor's profile and its local override, logcheck's rules, logrotate's
# rule, mecab's configuration (a dependency of the server package), and the
# link that enables the unit.
^/etc/mysql(/|$)
^/etc/alternatives/my\.cnf$
^/etc/apparmor\.d/(usr\.sbin\.mysqld|local/usr\.sbin\.mysqld)$
^/etc/logcheck/ignore\.d\.(paranoid|server|workstation)(/mysql-server)?$
^/etc/logrotate\.d/mysql-server$
^/etc/mecabrc$
^/etc/systemd/system/multi-user\.target\.wants/mysql\.service$
# The system users and groups the two packages add (mysql, valkey), and the
# linker cache their libraries refresh.
^/etc/(passwd|group|shadow|gshadow)$
^/etc/ld\.so\.cache$
# Valkey's package: the package's configuration, which stays as it is (/etc/valkey),
# the configuration and the ACL file the agent renders and the admin
# password (/etc/zimam/valkey), the drop-in that points the unit at them, the
# unit's alias and enabling link, the init script with its defaults and rc
# links, and logrotate's rule.
^/etc/valkey(/|$)
^/etc/zimam/valkey(/|$)
^/etc/systemd/system/valkey-server\.service\.d(/|$)
^/etc/systemd/system/valkey\.service$
^/etc/systemd/system/multi-user\.target\.wants/valkey-server\.service$
^/etc/init\.d/valkey-server$
^/etc/default/valkey-server$
^/etc/rc[0-6]\.d/[KS]01valkey-server$
^/etc/logrotate\.d/valkey-server$
EOF
}

case ${1:-} in
snapshot)
	when=${2:?usage: snapshot before|after}
	lxc file push -q --mode 0755 "$E2E_ROOT/e2e/scripts/fs-hash.sh" "$E2E_CONTAINER$fshash"
	in_c "$fshash" >"$dir/fs-$when.txt"
	if [[ $when == after ]]; then
		allowed_changes >"$dir/allowed.txt"
		python3 -I - "$dir/fs-before.txt" "$dir/fs-after.txt" "$dir/allowed.txt" <<'PY' || die "FAIL the two services changed paths that aren't on the list: see above"
import re, sys

def read(path):
    meta, hashes = {}, {}
    for line in open(path, encoding="utf-8", errors="surrogateescape").read().splitlines():
        parts = line.split("\t")
        if len(parts) == 5:
            meta[parts[0]] = tuple(parts[1:])
        elif "  " in line:
            digest, name = line.split("  ", 1)
            hashes[name] = digest
    return {p: (m, hashes.get(p)) for p, m in meta.items()}

before, after = read(sys.argv[1]), read(sys.argv[2])
allowed = [re.compile(l) for l in open(sys.argv[3]).read().splitlines() if l and not l.startswith("#")]
changed = sorted(p for p in before.keys() | after.keys() if before.get(p) != after.get(p))
outside = [p for p in changed if not any(r.search(p) for r in allowed)]
unused = [r.pattern for r in allowed if not any(r.search(p) for p in changed)]
for p in outside:
    print("FAIL  changed outside the list:", p, before.get(p), "->", after.get(p))
print(f"{len(changed)} paths changed under /etc, /root and /home; {len(outside)} are not on the list")
print("      list entries that matched nothing this run:", len(unused))
sys.exit(1 if outside else 0)
PY
		pass "installing and using MySQL and Valkey changed only the listed paths ($(wc -l <"$dir/fs-after.txt") lines compared)"
	fi
	;;
mem)
	label=${2:?usage: mem <label>}
	memory=$(free -m | awk '/^Mem:/ { printf "used %s MiB, available %s MiB of %s", $3, $7, $2 }')
	rss=$(in_c ps -eo comm=,rss= | awk '$1 == "mysqld" || $1 == "valkey-server" { r[$1] += $2 } END { printf "mysqld %d MiB, valkey-server %d MiB", r["mysqld"] / 1024, r["valkey-server"] / 1024 }')
	echo "$(date +%T) $label: $memory; $rss" | tee -a "$dir/memory.txt"
	;;
mark)
	jobs_mark >"$dir/mark"
	;;
wait)
	command=${2:?} count=${3:?} want=${4:?}
	wait_jobs "$(cat "$dir/mark")" "$command" "$count" "$want" || fail "the $command jobs didn't end $want"
	pass "$count $command job(s) ended $want"
	;;
installed)
	for unit in mysql valkey-server; do
		in_c systemctl is-active --quiet "$unit" || fail "$unit isn't running"
	done
	# Listening sockets: only loopback, and no MySQL X protocol.
	listening=$(in_c ss -Hltn | awk '{ print $4 }')
	for port in 3306 6379; do
		grep -qx "127.0.0.1:$port" <<<"$listening" || fail "nothing listens on 127.0.0.1:$port: $listening"
		! grep -Eq "^(0\.0\.0\.0|\*|\[::\]):$port\$" <<<"$listening" || fail "port $port listens beyond loopback: $listening"
	done
	! grep -q ':33060$' <<<"$listening" || fail "MySQL's X protocol listens: $listening"
	pass "MySQL and Valkey run and listen on loopback only (3306, 6379; 33060 is closed)"
	reply=$(in_c valkey-cli -e PING 2>&1 || true)
	[[ $reply == *NOAUTH* || $reply == *"Authentication required"* ]] || fail "an unauthenticated Valkey client got: $reply"
	reply=$(in_c valkey-cli --user default --pass nothing-here-at-all -e PING 2>&1 || true)
	[[ $reply == *WRONGPASS* || $reply == *"invalid"* ]] || fail "Valkey's default user logs in: $reply"
	pass "Valkey's default user is off: $reply"
	version=$(mysql_root 'SELECT @@version')
	[[ $version == 8.4.* ]] || fail "MySQL is $version, not 8.4"
	pass "MySQL $version, bind $(mysql_root 'SELECT @@bind_address'), local_infile $(mysql_root 'SELECT @@local_infile')"
	;;
authorize)
	site=${2:?}
	key=$(panel_sql 'select r.deploy_key_public as k from site_repositories r join sites s on s.id = r.site_id where s.name = ?' "$site" |
		python3 -I -c 'import json, sys; print(json.load(sys.stdin)[0]["k"] or "")')
	[[ $key == ssh-ed25519\ * ]] || fail "the panel has no deploy key for $site: $key"
	bash "$githost" authorize "$key"
	;;
env)
	site=${2:?}
	user=$(user_of "$site")
	file=/home/$user/site/shared/.env
	mode=$(in_c stat -c '%U %a' "$file")
	[[ $mode == "$user 600" ]] || fail "$file is $mode, want $user 600"
	env=$(in_c runuser -u "$user" -- cat "$file")
	mysql_password=$(cat "$(pw "$site" mysql)")
	valkey_password=$(cat "$(pw "$site" valkey)")
	for want in DB_CONNECTION=mysql DB_HOST=127.0.0.1 DB_PORT=3306 "DB_DATABASE=$(database_of "$site")" "DB_USERNAME=$user" \
		"DB_PASSWORD=$mysql_password" REDIS_CLIENT=phpredis REDIS_HOST=127.0.0.1 REDIS_PORT=6379 "REDIS_USERNAME=$user" \
		"REDIS_PASSWORD=$valkey_password" "REDIS_PREFIX=$user:" CACHE_STORE=redis QUEUE_CONNECTION=redis; do
		grep -qxF "$want" <<<"$env" || fail "$file has no line ${want%%=*}= with the expected value (${want%%=*}: $(grep -E "^${want%%=*}=" <<<"$env" | sed 's/PASSWORD=.*/PASSWORD=<…>/'))"
	done
	grep -q '^APP_KEY=base64:' <<<"$env" || fail "the .env lost its APP_KEY"
	pass "$site's .env has the DB_ and REDIS_ keys, REDIS_PREFIX=$user:, and the saved passwords; CACHE_STORE and QUEUE_CONNECTION are the site's own, redis"
	;;
serves)
	site=${2:?}
	reply=$(get "$site")
	[[ $(code_of "$reply") == 200 && $(body_of "$reply") == "Zimam e2e app release="* ]] || fail "$site answers: $reply"
	pass "$site serves: $(body_of "$reply")"
	;;
uses)
	site=${2:?} word=${3:?}
	user=$(user_of "$site")
	database=$(database_of "$site")
	reply=$(get "$site" "?db=$word")
	[[ $(code_of "$reply") == 200 && $(body_of "$reply") == "database=$database user=$user row=$word" ]] || fail "$site's ?db= answers: $reply"
	pass "$site wrote a row to $database as $user and read it back: $(body_of "$reply")"
	reply=$(get "$site" "?cache=$word")
	[[ $(code_of "$reply") == 200 && $(body_of "$reply") == "prefix=$user: user=$user cache=$word" ]] || fail "$site's ?cache= answers: $reply"
	pass "$site wrote a cache key and read it back: $(body_of "$reply")"
	# Only the site's own database holds the row.
	[[ $(mysql_root "SELECT COUNT(*) FROM \`$database\`.e2e_rows WHERE word = '$word'") == 1 ]] || fail "$database doesn't hold the row $word"
	for other in $(mysql_root "SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 's\_%'"); do
		[[ $other != "$database" ]] || continue
		[[ $(mysql_root "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '$other' AND table_name = 'e2e_rows'") == 1 ]] || continue
		[[ $(mysql_root "SELECT COUNT(*) FROM \`$other\`.e2e_rows WHERE word = '$word'") == 0 ]] || fail "$other holds $site's row $word"
	done
	[[ $(valkey_admin -n 1 EXISTS "$user:e2e:cache:$word") == 1 ]] || fail "Valkey database 1 has no $user:e2e:cache:$word"
	pass "the row is in $database only, and the cache key $user:e2e:cache:$word is in Valkey database 1"
	reply=$(get "$site" "?queue=$word")
	[[ $(code_of "$reply") == 200 && $(body_of "$reply") == "queued=$word" ]] || fail "$site's ?queue= answers: $reply"
	if [[ -f $dir/worker-$site.pid ]]; then
		for _ in $(seq 1 30); do
			reply=$(get "$site" "?done=$word")
			[[ $(body_of "$reply") == done=worker ]] && break
			sleep 1
		done
		[[ $(body_of "$reply") == done=worker ]] || fail "the queue worker didn't run the job $word: $reply"
		pass "a queue job round-tripped through Valkey: pushed by the app, run by the worker as $user"
	else
		[[ $(keys_in "$site" 0) -ge 1 ]] || fail "the queued job left no key under $user: in Valkey database 0"
		pass "a queue job is waiting in Valkey, under $user: in database 0: $(body_of "$reply")"
	fi
	;;
stale)
	site=${2:?}
	for what in "db=stale" "cache=stale"; do
		reply=$(get "$site" "?$what")
		[[ $(code_of "$reply") == 500 && $(body_of "$reply") == error:* ]] || fail "$site's ?$what with the old password answers: $reply"
		pass "$site's cached config still holds the old password, and ?$what is refused: $(body_of "$reply" | cut -c1-120)"
	done
	;;
logins)
	site=${2:?} which=${3:?usage: logins <site> new|old}
	user=$(user_of "$site")
	database=$(database_of "$site")
	good='' bad=''
	name=new
	if [[ $which == new ]]; then bad=.old; else good=.old name=old; fi
	for protocol in socket tcp; do
		out=$(mysql_as "$user" "$(pw "$site" mysql)$good" "$protocol" 'SELECT CURRENT_USER()' 2>&1) || fail "$user's saved${good:+ old} MySQL password is refused over $protocol: $out"
		[[ $out == "$user@"* ]] || fail "$user over $protocol is $out"
		who=$out
		if [[ -f $(pw "$site" mysql)$bad ]]; then
			out=$(mysql_as "$user" "$(pw "$site" mysql)$bad" "$protocol" 'SELECT 1' 2>&1) && fail "$user's other MySQL password still works over $protocol"
			[[ $out == *"Access denied for user '$user'@"* ]] || fail "$user's other MySQL password over $protocol answered: $out"
			pass "MySQL over $protocol: $name password works as $who; the other is refused: ${out:0:90}"
		fi
	done
	[[ $(mysql_as "$user" "$(pw "$site" mysql)$good" tcp "SHOW DATABASES LIKE '$database'") == "$database" ]] || fail "$user can't see its own database"
	out=$(valkey_as "$user" "$(pw "$site" valkey)$good" PING 2>&1) || fail "$user's Valkey password is refused: $out"
	[[ $out == PONG ]] || fail "Valkey answered $out"
	if [[ -f $(pw "$site" valkey)$bad ]]; then
		out=$(valkey_as "$user" "$(pw "$site" valkey)$bad" PING 2>&1) && fail "$user's other Valkey password still works"
		[[ $out == *WRONGPASS* ]] || fail "$user's other Valkey password answered: $out"
		pass "Valkey: the $name password works; the other is refused: $out"
	fi
	;;
worker)
	site=${2:?} what=${3:?usage: worker <site> start|alive|dead}
	user=$(user_of "$site")
	php=/usr/bin/php8.4
	case $what in
	start)
		rm -f "$dir/worker-$site.pid"
		in_c bash -c "rm -f /var/tmp/zimam-e2e-worker-$site.log; cd /home/$user/site/current && setsid nohup runuser -u $user -- $php artisan queue:work >/var/tmp/zimam-e2e-worker-$site.log 2>&1 </dev/null & echo \$! >/var/tmp/zimam-e2e-worker-$site.pid"
		sleep 2
		in_c bash -c "pgrep -u $user -f 'artisan queue:work' >/var/tmp/zimam-e2e-worker-$site.pid" || fail "the worker of $site didn't start: $(in_c cat "/var/tmp/zimam-e2e-worker-$site.log")"
		in_c cat "/var/tmp/zimam-e2e-worker-$site.pid" >"$dir/worker-$site.pid"
		pass "a queue worker runs as $user (pid $(head -n 1 "$dir/worker-$site.pid"))"
		;;
	alive)
		in_c pgrep -u "$user" -f 'artisan queue:work' >/dev/null || fail "$site's worker isn't running: $(in_c cat "/var/tmp/zimam-e2e-worker-$site.log")"
		pass "$site's worker is running"
		;;
	dead)
		for _ in $(seq 1 30); do
			in_c pgrep -u "$user" -f 'artisan queue:work' >/dev/null || break
			sleep 1
		done
		! in_c pgrep -u "$user" -f 'artisan queue:work' >/dev/null || fail "$site's worker is still running after its Valkey password changed"
		log=$(in_c cat "/var/tmp/zimam-e2e-worker-$site.log")
		pass "$site's worker ended when its password changed: $log"
		rm -f "$dir/worker-$site.pid"
		;;
	*) die "usage: worker <site> start|alive|dead" ;;
	esac
	;;
scratch)
	site=${2:?} what=${3:?usage: scratch <site> present|gone}
	user=$(user_of "$site")
	database=s_${site}_scratch
	want=0
	[[ $what == present ]] && want=1
	[[ $(mysql_root "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$database'") == "$want" ]] || fail "the database $database is not $what"
	# Two accounts, a grant on each database for both.
	[[ $(mysql_root "SELECT COUNT(*) FROM mysql.db WHERE user = '$user'") == $((2 + want * 2)) ]] || fail "$user has $(mysql_root "SELECT COUNT(*) FROM mysql.db WHERE user = '$user'") grants on databases with the scratch database $what"
	if [[ $what == present ]]; then
		[[ -z $(mysql_as "$user" "$(pw "$site" mysql)" tcp "CREATE TABLE \`$database\`.t (x INT); INSERT INTO \`$database\`.t VALUES (1)") ]] || fail "$user can't use $database"
		pass "$database exists, and $user (given its grant when the database was made) uses it"
	else
		out=$(mysql_as "$user" "$(pw "$site" mysql)" tcp "USE \`$database\`") && fail "$user still uses $database"
		[[ $out == *"ERROR 1049"* || $out == *"ERROR 1044"* ]] || fail "$user's USE of the dropped $database answered: $out"
		pass "$database is gone with its grants, and $user is refused: ${out:0:100}"
	fi
	;;
gone | kept)
	site=${2:?}
	user=$(user_of "$site")
	database=$(database_of "$site")
	want=0
	[[ $1 == gone ]] || want=1
	[[ $(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE user = '$user'") == $((want * 2)) ]] || fail "$user has $(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE user = '$user'") MySQL accounts, want $((want * 2))"
	[[ $(mysql_root "SELECT COUNT(*) FROM mysql.db WHERE user = '$user'") == $((want * 2)) ]] || fail "$user has the wrong number of MySQL grants on databases"
	[[ $(mysql_root "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$database'") == "$want" ]] || fail "the database $database is not as it should be"
	[[ $(in_c grep -c "^user $user " /etc/zimam/valkey/users.acl || true) == "$want" ]] || fail "/etc/zimam/valkey/users.acl has the wrong number of lines for $user"
	[[ $(valkey_admin ACL LIST | grep -c "^user $user ") == "$want" ]] || fail "Valkey's ACL has the wrong number of lines for $user"
	if [[ $1 == gone ]]; then
		[[ $(keys_of "$site") == 0 ]] || fail "$site left keys in Valkey: $(keys_in "$site" 0) in database 0, $(keys_in "$site" 1) in database 1"
		! in_c test -e "/home/$user" || fail "/home/$user is still there"
		pass "$site is gone: no MySQL account, grant or database, no Valkey user, no key in any of the 16 Valkey databases, no home"
	else
		[[ $(keys_of "$site") -ge 1 ]] || fail "$site's keys are gone from Valkey"
		in_c test -d "/home/$user" || fail "/home/$user is gone"
		pass "$site is untouched: both MySQL accounts, the database, the Valkey user and its keys, its home"
	fi
	;;
*) die "usage: v03-databases.sh snapshot | mem | mark | wait | installed | authorize | env | serves | uses | stale | logins | worker | scratch | gone | kept" ;;
esac
