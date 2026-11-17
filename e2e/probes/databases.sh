#!/usr/bin/env bash
# The database and cache probes (v0.3.0), through the agent and as the sites'
# own users, on the throwaway static sites dbp-a and dbp-b, destroyed at the
# end; run-all.sh runs them between its two file snapshots, after the
# v03-databases scenario installed MySQL and Valkey. A site's MySQL user and
# Valkey user must stay inside the site:
#   - as dbp-a, over the socket and over TCP: another site's database can't
#     be used or read (not even one its grant pattern would match if its
#     underscores were wildcards), SHOW DATABASES lists only its own, no file
#     is written (SELECT ... INTO OUTFILE) or read (LOAD DATA, LOAD DATA
#     LOCAL INFILE, the server's local_infile being off), and no global
#     statement runs (CREATE USER, GRANT, SET GLOBAL, the mysql schema);
#     root's account isn't open over TCP;
#   - as dbp-a in Valkey: another site's keys, CONFIG, ACL, FLUSHALL,
#     FLUSHDB, KEYS, FUNCTION, SCRIPT FLUSH, SCRIPT KILL, SCRIPT DEBUG,
#     SHUTDOWN, replication and another site's
#     channel are refused, in database 0 and in database 1, and scripts can't
#     reach another site's keys, declared or not; its own keys work, scripts
#     included; the default user is off and a wrong password is refused;
#   - MySQL and Valkey answer on the loopback only, not on the server's
#     address, from the server itself and from this host;
#   - a site that still has databases isn't destroyed without them; destroyed
#     with them, a site leaves no MySQL account, grant or database, no Valkey
#     user and no key in any Valkey database, while the other site is untouched.
# The rejections of malformed names and passwords are validation.sh's.
# shellcheck source=../scripts/services.sh
source "$(dirname "$0")/../scripts/services.sh"
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
load_state

a=dbp-a
b=dbp-b
ua=s_$a
ub=s_$b
da=s_dbp_a_main
db=s_dbp_b_main
# The underscores in a grant's database name are wildcards unless escaped:
# this database matches the pattern of dbp-a's grant, and isn't dbp-a's.
lookalike=s_dbp_axmain
work=$E2E_WORK/probes-databases
rm -rf "$work"
mkdir -p "$work"
chmod 0700 "$work"
outfile=/tmp/dbp-outfile
localfile=/tmp/dbp-local.txt

for unit in mysql valkey-server; do
	in_c systemctl is-active --quiet "$unit" || die "$unit isn't running: run scenarios/v03-databases.sh first"
done

# denied <name> <pattern> <output> passes when the output holds the refusal
# the pattern names, and shows it.
denied() {
	if [[ $3 =~ $2 ]]; then
		pass "$1 is refused: $(head -n 1 <<<"$3" | cut -c1-160)"
	else
		fail "$1 answered: ${3:0:300}"
	fi
}

# allowed <name> <want> <output> passes when the output is what the
# statement should have answered.
allowed() {
	if [[ $3 == "$2" ]]; then
		pass "$1 answers $2"
	else
		fail "$1 answered ${3:0:300}, want $2"
	fi
}

password() {
	python3 -I -c 'import secrets; print(secrets.token_urlsafe(32))'
}

# A site: provisioned, with a database, a MySQL user and a Valkey user.
setup_site() {
	local site=$1 database=$2 pass
	pass=$(password)
	printf '%s\n' "$pass" >"$work/$site-mysql.pw"
	pass=$(password)
	printf '%s\n' "$pass" >"$work/$site-valkey.pw"
	run_job site.provision "{\"name\":\"$site\",\"domain\":\"$site.$E2E_DOMAIN\",\"type\":\"static\"}"
	run_job db.create "{\"site\":\"$site\",\"database\":\"$database\"}"
	run_job db.user "{\"site\":\"$site\",\"password\":\"$(cat "$work/$site-mysql.pw")\"}"
	run_job cache.user "{\"site\":\"$site\",\"password\":\"$(cat "$work/$site-valkey.pw")\"}"
}

setup_site "$a" "$da"
setup_site "$b" "$db"
pass "$a and $b have a database, a MySQL user and a Valkey user"

# b's table, and its Valkey keys in the default and the cache database.
out=$(mysql_as "$ub" "$work/$b-mysql.pw" socket "CREATE TABLE \`$db\`.secrets (x TEXT); INSERT INTO \`$db\`.secrets VALUES ('b-only')" || true)
[[ -z $out ]] || die "b couldn't fill its table: $out"
for n in 0 1; do
	out=$(valkey_as "$ub" "$work/$b-valkey.pw" -n "$n" SET "$ub:x" b-only || true)
	[[ $out == OK ]] || die "b couldn't set its key in database $n: $out"
done
# A database whose name the unescaped grant pattern of dbp-a would match.
mysql_root "CREATE DATABASE IF NOT EXISTS \`$lookalike\`; CREATE TABLE \`$lookalike\`.secrets (x TEXT); INSERT INTO \`$lookalike\`.secrets VALUES ('not-a')"

in_c bash -c "echo local >$localfile && chmod 0644 $localfile"

# MySQL, as dbp-a.
for protocol in socket tcp; do
	p=$work/$a-mysql.pw
	out=$(mysql_as "$ua" "$p" "$protocol" 'SELECT CURRENT_USER()') || true
	if [[ $out == "$ua@"* ]]; then
		pass "over $protocol, dbp-a logs in as $out"
	else
		fail "over $protocol, dbp-a's login answered $out"
	fi
	out=$(mysql_as "$ua" "$p" "$protocol" "CREATE TABLE IF NOT EXISTS \`$da\`.t (x TEXT); DELETE FROM \`$da\`.t; INSERT INTO \`$da\`.t VALUES ('a-own'); SELECT x FROM \`$da\`.t") || true
	allowed "over $protocol, dbp-a's own table" a-own "$out"
	out=$(mysql_as "$ua" "$p" "$protocol" 'SHOW DATABASES') || true
	allowed "over $protocol, SHOW DATABASES" "$(printf 'information_schema\nperformance_schema\n%s' "$da")" "$out"
	denied "over $protocol, USE another site's database" "ERROR 1044 .*Access denied for user '$ua'@'[^']*' to database '$db'" "$(mysql_as "$ua" "$p" "$protocol" "USE \`$db\`")"
	denied "over $protocol, reading another site's table" "ERROR 1142 .*SELECT command denied" "$(mysql_as "$ua" "$p" "$protocol" "SELECT x FROM \`$db\`.secrets")"
	denied "over $protocol, USE a database the grant pattern matches with wildcards" "ERROR 1044 .*Access denied for user '$ua'@'[^']*' to database '$lookalike'" "$(mysql_as "$ua" "$p" "$protocol" "USE \`$lookalike\`")"
	denied "over $protocol, reading that database's table" "ERROR 1142 .*SELECT command denied" "$(mysql_as "$ua" "$p" "$protocol" "SELECT x FROM \`$lookalike\`.secrets")"
	denied "over $protocol, SELECT ... INTO OUTFILE" "ERROR (1045|1227) .*Access denied" "$(mysql_as "$ua" "$p" "$protocol" "SELECT x FROM \`$da\`.t INTO OUTFILE '$outfile'")"
	denied "over $protocol, LOAD DATA INFILE on the server" "ERROR 1045 .*Access denied for user '$ua'@" "$(mysql_as "$ua" "$p" "$protocol" "LOAD DATA INFILE '$localfile' INTO TABLE \`$da\`.t")"
	denied "over $protocol, LOAD DATA LOCAL INFILE, the client allowing it" "ERROR 3948 .*Loading local data is disabled" "$(mysql_as "$ua" "$p" "$protocol" "LOAD DATA LOCAL INFILE '$localfile' INTO TABLE \`$da\`.t" --local-infile=1)"
	denied "over $protocol, reading the mysql schema" "ERROR 1142 .*SELECT command denied" "$(mysql_as "$ua" "$p" "$protocol" 'SELECT user FROM mysql.user')"
	denied "over $protocol, CREATE USER" "ERROR 1227 .*Access denied; you need" "$(mysql_as "$ua" "$p" "$protocol" "CREATE USER 'dbp-x'@'localhost'")"
	denied "over $protocol, GRANT on every database" "ERROR (1044|1045) .*Access denied" "$(mysql_as "$ua" "$p" "$protocol" "GRANT ALL ON *.* TO '$ua'@'localhost'")"
	denied "over $protocol, CREATE DATABASE" "ERROR 1044 .*Access denied" "$(mysql_as "$ua" "$p" "$protocol" 'CREATE DATABASE dbp_x')"
	denied "over $protocol, SET GLOBAL local_infile" "ERROR 1227 .*Access denied; you need" "$(mysql_as "$ua" "$p" "$protocol" 'SET GLOBAL local_infile = 1')"
	denied "over $protocol, a wrong password" "ERROR 1045 .*Access denied for user '$ua'@" "$(mysql_as "$ua" "$work/$b-mysql.pw" "$protocol" 'SELECT 1')"
done
if in_c test -e "$outfile"; then
	fail "$outfile was written"
else
	pass "$outfile was never written"
fi
if in_c bash -c 'ls /var/lib/mysql-files | grep -q .'; then
	fail "MySQL's secure_file_priv folder holds files: $(in_c ls /var/lib/mysql-files)"
else
	pass "MySQL's secure_file_priv folder is empty"
fi
echo "" >"$work/empty.pw"
denied "over TCP, root with no password" "ERROR 1045 .*Access denied for user 'root'@" "$(mysql_as root "$work/empty.pw" tcp 'SELECT 1')"
denied "over the socket, $ua with no password" "ERROR 1045 .*Access denied for user '$ua'@" "$(mysql_as "$ua" "$work/empty.pw" socket 'SELECT 1')"
# What dbp-b holds is still what b put there.
allowed "dbp-b's own table, after all that" b-only "$(mysql_as "$ub" "$work/$b-mysql.pw" tcp "SELECT x FROM \`$db\`.secrets")"

# Valkey, as dbp-a (key prefix s_dbp-a:).
va() {
	valkey_as "$ua" "$work/$a-valkey.pw" "$@" || true
}
allowed "Valkey PING" PONG "$(va PING)"
for n in 0 1; do
	allowed "SET on its own key in database $n" OK "$(va -n "$n" SET "$ua:x" a-only)"
	allowed "GET on its own key in database $n" a-only "$(va -n "$n" GET "$ua:x")"
	denied "GET on another site's key in database $n" '^NOPERM' "$(va -n "$n" GET "$ub:x")"
	denied "SET on another site's key in database $n" '^NOPERM' "$(va -n "$n" SET "$ub:x" overwritten)"
	denied "DEL on another site's key in database $n" '^NOPERM' "$(va -n "$n" DEL "$ub:x")"
done
allowed "an own-key script" OK "$(va EVAL "return redis.call('set', KEYS[1], 'v')" 1 "$ua:script")"
denied "EVAL with another site's key declared" '^NOPERM' "$(va EVAL "return redis.call('get', KEYS[1])" 1 "$ub:x")"
denied "EVAL reaching another site's key it didn't declare" 'NOPERM|ACL failure|no permissions|[Cc]an.t access' "$(va EVAL "return redis.call('get', 's_dbp-b:x')" 0)"
denied "EVAL writing another site's key it didn't declare" 'NOPERM|ACL failure|no permissions|[Cc]an.t access' "$(va EVAL "return redis.call('set', 's_dbp-b:x', 'overwritten')" 0)"
denied "CONFIG SET" '^NOPERM' "$(va CONFIG SET maxmemory 1)"
denied "CONFIG GET" '^NOPERM' "$(va CONFIG GET '*')"
denied "ACL LIST" '^NOPERM' "$(va ACL LIST)"
denied "ACL SETUSER" '^NOPERM' "$(va ACL SETUSER dbp-x on '>pw' '~*' '+@all')"
denied "FLUSHALL" '^NOPERM' "$(va FLUSHALL)"
denied "FLUSHDB" '^NOPERM' "$(va FLUSHDB)"
denied "KEYS *" '^NOPERM' "$(va KEYS '*')"
denied "FUNCTION LIST" '^NOPERM' "$(va FUNCTION LIST)"
denied "FUNCTION FLUSH" '^NOPERM' "$(va FUNCTION FLUSH)"
denied "SCRIPT FLUSH" '^NOPERM' "$(va SCRIPT FLUSH)"
denied "SCRIPT KILL" '^NOPERM' "$(va SCRIPT KILL)"
denied "SCRIPT DEBUG" '^NOPERM' "$(va SCRIPT DEBUG NO)"
denied "PUBLISH on another site's channel" '^NOPERM' "$(va PUBLISH "$ub:channel" hi)"
denied "SHUTDOWN" '^NOPERM' "$(va SHUTDOWN NOSAVE)"
denied "REPLICAOF" '^NOPERM' "$(va REPLICAOF 192.0.2.1 6379)"
denied "DEBUG" 'NOPERM|DEBUG command not allowed' "$(va DEBUG SLEEP 0)"
allowed "dbp-b's keys, after all that" b-only "$(valkey_as "$ub" "$work/$b-valkey.pw" GET "$ub:x")"
allowed "dbp-b's keys in database 1, after all that" b-only "$(valkey_as "$ub" "$work/$b-valkey.pw" -n 1 GET "$ub:x")"
denied "a login with the default user" 'WRONGPASS|invalid|Authentication required' "$(in_c valkey-cli --user default --pass whatever-it-is-123 -e PING 2>&1 || true)"
denied "a client with no login" 'NOAUTH|Authentication required' "$(in_c valkey-cli -e PING 2>&1 || true)"
denied "dbp-a with dbp-b's password" 'WRONGPASS' "$(valkey_as "$ua" "$work/$b-valkey.pw" PING || true)"

# Nothing listens on the server's address.
# shellcheck disable=SC2153 # load_state reads CIP from state.env
addresses=("$CIP")
[[ -z ${CIP6:-} ]] || addresses+=("$CIP6")
for address in "${addresses[@]}"; do
	for port in 3306 6379 33060; do
		if timeout 5 bash -c "exec 3<>/dev/tcp/$address/$port" 2>/dev/null; then
			fail "$address:$port answers from this host"
		else
			pass "$address:$port doesn't answer from this host"
		fi
		if in_c timeout 5 bash -c "exec 3<>/dev/tcp/$address/$port" 2>/dev/null; then
			fail "$address:$port answers from the server itself"
		else
			pass "$address:$port doesn't answer from the server itself"
		fi
	done
done
listening=$(in_c ss -Hltn | awk '{ print $4 }')
if grep -Eq ':(3306|6379|33060)$' <<<"$listening" && grep -E ':(3306|6379|33060)$' <<<"$listening" | grep -Evq '^(127\.0\.0\.1|\[::1\]):'; then
	fail "MySQL or Valkey listens beyond loopback: $(grep -E ':(3306|6379|33060)$' <<<"$listening" | tr '\n' ' ')"
else
	pass "MySQL and Valkey listen on loopback only: $(grep -E ':(3306|6379|33060)$' <<<"$listening" | tr '\n' ' ')"
fi

# A site with databases isn't destroyed without them.
probe "site.destroy of a site that still has databases" conflict "{$env_ok,\"command\":\"site.destroy\",\"params\":{\"name\":\"$a\"}}"
in_c test -d "/home/$ua" || fail "$a's home went with a refused destroy"
allowed "$a's database, after the refused destroy" a-own "$(mysql_as "$ua" "$work/$a-mysql.pw" socket "SELECT x FROM \`$da\`.t")"

# Destroyed with them, b leaves nothing in MySQL or Valkey, and a is untouched.
run_job site.destroy "{\"name\":\"$b\",\"drop_databases\":true}"
allowed "MySQL accounts of $ub" 0 "$(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE user = '$ub'")"
allowed "MySQL grants of $ub" 0 "$(mysql_root "SELECT COUNT(*) FROM mysql.db WHERE user = '$ub'")"
allowed "the database $db" 0 "$(mysql_root "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$db'")"
allowed "Valkey's ACL lines of $ub" 0 "$(valkey_admin ACL LIST | grep -c "^user $ub " || true)"
allowed "$ub's lines in users.acl" 0 "$(in_c grep -c "^user $ub " /etc/zimam/valkey/users.acl || true)"
for n in 0 1; do
	allowed "$ub's keys in Valkey database $n" 0 "$(keys_in "$b" "$n")"
done
allowed "$ub's keys in all 16 Valkey databases" 0 "$(keys_of "$b")"
allowed "$a's MySQL accounts" 2 "$(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE user = '$ua'")"
allowed "$a's key in Valkey database 1" a-only "$(va -n 1 GET "$ua:x")"
allowed "$a's table" a-own "$(mysql_as "$ua" "$work/$a-mysql.pw" tcp "SELECT x FROM \`$da\`.t")"

run_job site.destroy "{\"name\":\"$a\",\"drop_databases\":true}"
allowed "MySQL accounts of $ua" 0 "$(mysql_root "SELECT COUNT(*) FROM mysql.user WHERE user = '$ua'")"
allowed "$ua's keys in all 16 Valkey databases" 0 "$(keys_of "$a")"
allowed "the look-alike database" 1 "$(mysql_root "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '$lookalike'")"

mysql_root "DROP DATABASE \`$lookalike\`"
in_c rm -f "$outfile" "$localfile"
rm -rf "$work"
exit "$failed"
