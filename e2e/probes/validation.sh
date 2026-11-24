#!/usr/bin/env bash
# Validation probes (design §14.4), sent as zimam for milestone 2's
# commands (bad site names, domains and PHP versions), milestone 3's
# site.security, every field of v0.2.0's six commands, and v0.4.0's worker,
# cron and Node.js commands, and v0.5.0's backup commands: each answers validation_failed before anything
# runs, and leaves exactly one error:validation_failed audit entry.
# run-all.sh runs them between its two file snapshots.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

# send <name> <command> <params> probes one request.
send() {
	probe "$1" validation_failed "{$env_ok,\"command\":\"$2\",\"params\":$3}"
}

# send_field <name> <command> <params> <field> also wants the reply to name
# the field.
send_field() {
	probe "$1" validation_failed "{$env_ok,\"command\":\"$2\",\"params\":$3}" "$4"
}

# Site names: dots, slashes, .., uppercase, Unicode, 25 characters, and
# the reserved names.
long=$(printf 'a%.0s' {1..25})
for name in shop.example s/hop .. ../etc Shop 'café' "$long" zimam panel default acme; do
	send "site.provision with the name $name" site.provision "{\"name\":\"$name\",\"domain\":\"vprobe.zimam.test\",\"type\":\"static\"}"
done
# Domains: a space, ;, {, a newline, $, quotes and a wildcard, each as it
# appears in the JSON, so the newline and the double quote are escaped.
# shellcheck disable=SC2016 # the $ is the probe
for domain in 'a b.zimam.test' 'a;b.zimam.test' 'a{b.zimam.test' 'a\nb.zimam.test' 'a$b.zimam.test' 'a\"b.zimam.test' "a'b.zimam.test" '*.zimam.test'; do
	send "site.provision with the domain $domain" site.provision "{\"name\":\"vprobe\",\"domain\":\"$domain\",\"type\":\"static\"}"
done
# PHP versions: a command after the version, a path, and one before the
# allowlist.
for v in '8.5; rm -rf /' '../8.4' '8.1'; do
	send "php.install of $v" php.install "{\"version\":\"$v\"}"
	send "site.provision on PHP $v" site.provision "{\"name\":\"vprobe\",\"domain\":\"vprobe.zimam.test\",\"type\":\"php\",\"php_version\":\"$v\"}"
	send "site.php to $v" site.php "{\"name\":\"php84\",\"php_version\":\"$v\"}"
done
# The other site commands take a name only.
for command in site.destroy cert.issue; do
	for name in ../php84 'php84/../..' PHP84; do
		send "$command of $name" "$command" "{\"name\":\"$name\"}"
	done
done
# site.security (milestone 3): php84's valid settings with one bad entry.
# IP lists: a prefix past 32 or 128, host bits set, a bad address, a
# hostname, an address with a port, and a command after the address.
security() {
	local allow=$1 deny=$2 users=$3
	echo "{\"name\":\"php84\",\"force_https\":true,\"hsts\":{\"max_age\":null,\"include_subdomains\":false},\"headers_preset\":false,\"block_risky_functions\":false,\"restrict_to_site_folder\":false,\"ip_allow\":$allow,\"ip_deny\":$deny,\"password_protection\":$users}"
}
off='{"enabled":false,"users":[]}'
for cidr in 10.0.0.0/33 '2001:db8::/129' 10.0.0.1/8 300.1.2.3 10.0.0 example.com 10.0.0.1:80 '10.0.0.1; deny all' '10.0.0.1/-1' '10.0.0.1/ 8'; do
	send_field "site.security allowing $cidr" site.security "$(security "[\"$cidr\"]" '[]' "$off")" ip_allow
	send_field "site.security denying $cidr" site.security "$(security '[]' "[\"$cidr\"]" "$off")" ip_deny
done
# Password hashes: plain text, MD5, bcrypt's $2a$ and $2b$ prefixes, a cost
# outside 10–14, and a bcrypt hash one character short.
salt=abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0
# shellcheck disable=SC2016 # the $s are the hashes' own
for hash in hunter2 5f4dcc3b5aa765d61d8327deb882cf99 "\$2a\$12\$$salt" "\$2b\$12\$$salt" "\$2y\$04\$$salt" "\$2y\$15\$$salt" "\$2y\$12\$${salt:1}"; do
	send_field "site.security with the hash $hash" site.security \
		"$(security '[]' '[]' "{\"enabled\":true,\"users\":[{\"username\":\"e2e\",\"password_hash\":\"$hash\"}]}")" password_protection
done
# Usernames: a colon, which would split the htpasswd line, and a newline
# (escaped in the JSON), which would add one; the hash is valid.
for name in 'a:b' 'a\nb' 'a\rb' ''; do
	send_field "site.security with the username '$name'" site.security \
		"$(security '[]' '[]' "{\"enabled\":true,\"users\":[{\"username\":\"$name\",\"password_hash\":\"\$2y\$12\$$salt\"}]}")" password_protection
done

# v0.2.0's six commands, every field. A JSON type the field can't take
# fails the decode, which names params rather than the field.
# json_str prints its argument as a JSON string.
json_str() {
	python3 -I -c 'import json, sys; print(json.dumps(sys.argv[1]))' "$1"
}
repo() {
	echo "{\"name\":\"$1\",\"repository\":$(json_str "$2"),\"branch\":$(json_str "$3")$4}"
}
# site.repo: names, repositories (owner/name, no .git, no path, no space),
# branches (Git's ref rules) and rotate_key.
for name in ../laravel Laravel 'laravel/..'; do
	send_field "site.repo of $name" site.repo "$(repo "$name" zimam-e2e/laravel-app main '')" name
done
# shellcheck disable=SC2016 # $(id) is the probe
for r in octo/app.git -octo/app octo/.. octo/. 'octo/a b' octo '' ../etc/passwd octo/app/x 'octo/$(id)' "$(printf 'o%.0s' {1..40})/app"; do
	send_field "site.repo of the repository '$r'" site.repo "$(repo laravel "$r" main '')" repository
done
for b in -rf a..b refs/heads/../x .hidden/x x.lock a//b 'a@{1}' x/ x. '' 'a b' 'a;id' 'a~1' 'a^' 'a:b' '/main' "$(printf 'b%.0s' {1..201})"; do
	send_field "site.repo of the branch '$b'" site.repo "$(repo laravel zimam-e2e/laravel-app "$b" '')" branch
done
for v in '"yes"' 1 '""'; do
	send_field "site.repo with rotate_key $v" site.repo "$(repo laravel zimam-e2e/laravel-app main ",\"rotate_key\":$v")" params
done
# site.deploy: name, commit (40 lowercase hex), script (UTF-8, no NUL, at
# most 64 KiB), composer, trigger and keep_releases (1 to 20).
deploy() {
	echo "{\"name\":\"laravel\",\"composer\":true,\"trigger\":\"manual\"$1}"
}
send_field "site.deploy of ../laravel" site.deploy '{"name":"../laravel","composer":true,"trigger":"manual"}' name
for c in "$(printf 'A%.0s' {1..40})" "$(printf 'a%.0s' {1..39})" "$(printf 'a%.0s' {1..41})" HEAD main "$(printf 'g%.0s' {1..40})" ''; do
	send_field "site.deploy of the commit '$c'" site.deploy "$(deploy ",\"commit\":$(json_str "$c")")" commit
done
send_field "site.deploy with a NUL in its script" site.deploy "$(deploy ',"script":"php artisan migrate\u0000 --force"')" script
send_field "site.deploy with a script of 64 KiB + 1" site.deploy "$(deploy ",\"script\":\"$(printf 'x%.0s' $(seq 65537))\"")" script
for v in '"yes"' 1 '""'; do
	send_field "site.deploy with composer $v" site.deploy "{\"name\":\"laravel\",\"composer\":$v,\"trigger\":\"manual\"}" params
done
for t in push MANUAL '' 'manual ' webhooks; do
	send_field "site.deploy with the trigger '$t'" site.deploy "{\"name\":\"laravel\",\"composer\":true,\"trigger\":$(json_str "$t")}" trigger
done
for k in 0 21 -1 100; do
	send_field "site.deploy keeping $k releases" site.deploy "$(deploy ",\"keep_releases\":$k")" keep_releases
done
send_field "site.deploy keeping \"5\" releases" site.deploy "$(deploy ',"keep_releases":"5"')" params
# site.rollback: name and release (<14 digits>-<7 lowercase hex>).
send_field "site.rollback of ../laravel" site.rollback '{"name":"../laravel","release":"20261116000000-abcdef0"}' name
for r in ../initial initial / 20261116000000-ABCDEF0 20261116-abcdef0 '20261116000000-abcdef0/..' 20261116000000-abcdef01 ''; do
	send_field "site.rollback to the release '$r'" site.rollback "{\"name\":\"laravel\",\"release\":$(json_str "$r")}" release
done
# site.releases and site.env.read take a name only.
for command in site.releases site.env.read; do
	for name in ../laravel Laravel 'laravel/../..' ''; do
		send_field "$command of '$name'" "$command" "{\"name\":$(json_str "$name")}" name
	done
done
# site.env.write: name, and content (UTF-8, no NUL, at most 64 KiB).
send_field "site.env.write of ../laravel" site.env.write '{"name":"../laravel","content":"A=1\n"}' name
send_field "site.env.write with a NUL" site.env.write '{"name":"laravel","content":"APP_KEY=x\u0000\n"}' content
send_field "site.env.write of 64 KiB + 1" site.env.write "{\"name\":\"laravel\",\"content\":\"$(printf 'x%.0s' $(seq 65537))\"}" content
send_field "site.env.write with content 1" site.env.write '{"name":"laravel","content":1}' params

# v0.3.0's database and cache commands. The SQL and the ACL file the agent
# writes hold these values between backquotes and after a ">", so none of
# these may reach them: every one answers validation_failed, naming the field.
# db.create and db.drop: a database name of a site, with its prefix (php84's
# is s_php84_) and then lowercase letters, digits and underscores, at most 64
# characters, each underscore counting twice in a grant.
longname=$(printf 'x%.0s' {1..60})
# shellcheck disable=SC2016 # $(id) is the probe
for command in db.create db.drop; do
	for d in 's_php84_x`' 's_php84_x`; DROP DATABASE mysql; --' "s_php84_x'" 's_php84_x"' 's_php84_x;' 's_php84_x--' 's_php84_x -- y' \
		's_php84_x y' $'s_php84_x\n' $'s_php84_x\r' 's_php84_x.y' 's_php84_x/y' "s_php84_x\\" 's_php84_x%' 's_php84_x*' 's_php84_x$(id)' \
		's_php84_Main' 'S_php84_x' 's_php84_é' 's_php84_' 's_php84' 'x' 'main' 's_php85_x' 's_static_x' '../s_php84_x' '' \
		"s_php84_$longname" "s_php84_$(printf '_%.0s' {1..30})" mysql sys information_schema performance_schema zimam; do
		send_field "$command of the database '${d//$'\n'/\\n}'" "$command" "{\"site\":\"php84\",\"database\":$(json_str "$d")}" database
	done
	for name in ../php84 PHP84 'php84/..' ''; do
		send_field "$command of the site '$name'" "$command" "{\"site\":$(json_str "$name"),\"database\":\"s_php84_x\"}" site
	done
	send_field "$command with the database 1" "$command" '{"site":"php84","database":1}' params
done
for name in ../php84 PHP84 'php84/..'; do
	send_field "db.list of the site '$name'" db.list "{\"site\":$(json_str "$name")}" site
done
# db.user and cache.user: a password of 24 to 64 letters, digits, "_" and "-".
# One with a quote, a backslash or a newline would break out of the SQL
# literal or the ACL rule. The refusal names the field, and neither the reply
# nor the audit log holds the password.
pad=$(printf 'p%.0s' {1..30})
for command in db.user cache.user; do
	for p in "$pad'" "$pad\\" "$pad\'; DROP USER root" "$pad"$'\n' "$pad"$'\nx' "$pad\"" "$pad " "$pad;" "$pad\`" "$pad\$" "$pad%" "$pad:" "$pad#" "$pad>" \
		"$pad é" "$pad/" "$pad+" "$pad=" "$pad." \
		"$(printf 'p%.0s' {1..23})" "$(printf 'p%.0s' {1..65})" ''; do
		send_field "$command with the password '${p//$'\n'/\\n}'" "$command" "{\"site\":\"php84\",\"password\":$(json_str "$p")}" password
	done
	send_field "$command of the site '../php84'" "$command" "{\"site\":\"../php84\",\"password\":\"$(printf 'p%.0s' {1..30})\"}" site
	send_field "$command with the password 1" "$command" '{"site":"php84","password":1}' params
	# secret_probe: the refused password shows nowhere.
	marker=ZqMarker7e2e
	before=$(audit_lines)
	reply=$(request "$command" "{\"site\":\"php84\",\"password\":$(json_str "${marker}${pad}'")}")
	entries=$(audit_since "$before")
	if [[ $reply != *"$marker"* && $entries != *"$marker"* && $(field code <<<"$reply") == validation_failed ]]; then
		pass "$command's refused password is in neither the reply nor the audit log"
	else
		fail "$command's refused password shows: reply ${reply:0:200}; audit ${entries:0:300}"
	fi
done
# site.destroy's drop_databases is a boolean.
for v in '"yes"' 1 '""'; do
	send_field "site.destroy with drop_databases $v" site.destroy "{\"name\":\"php84\",\"drop_databases\":$v}" params
done
# v0.4.0's workers, cron jobs and Node.js. An owner's command is one line
# without NUL (R04-D1): a newline or a carriage return would end the
# script's line, or the cron file's, early. Each is sent JSON-escaped.
for c in 'php artisan horizon\nid' 'php artisan horizon\rid' 'php artisan horizon\u0000id' '' "$(printf 'x%.0s' {1..4097})"; do
	send_field "site.workers with a custom command '${c:0:40}'" site.workers \
		"{\"name\":\"php84\",\"workers\":[{\"id\":\"w\",\"kind\":\"custom\",\"processes\":1,\"command\":\"$c\"}]}" workers
	send_field "site.cron with the command '${c:0:40}'" site.cron \
		"{\"name\":\"php84\",\"scheduler\":false,\"jobs\":[{\"id\":\"j\",\"schedule\":\"* * * * *\",\"command\":\"$c\",\"enabled\":true}]}" jobs
done
# Schedules: @reboot and the other @ words cron takes beyond the five
# Zimam offers, a sixth field (the cron line's seventh, where the user
# would move to), one that names a user, a newline and a second line, a %,
# which cron turns into a newline, values out of range, and an empty one.
for s in '@reboot' '@midnight' '@annually' '* * * * * *' '* * * * * root' '0 0 * * * root id' '* * * * *\n* * * * * root id' '* * * * *\r' \
	'*/1 % * * *' '60 * * * *' '* 24 * * *' '* * 0 * *' '* * * 13 *' '* * * * 8' '*/0 * * * *' '* * * *' ' * * * * *' '*  * * * *' ''; do
	send_field "site.cron with the schedule '$s'" site.cron \
		"{\"name\":\"php84\",\"scheduler\":false,\"jobs\":[{\"id\":\"j\",\"schedule\":\"$s\",\"command\":\"true\",\"enabled\":true}]}" jobs
done
send_field "site.cron with a job named scheduler" site.cron \
	'{"name":"php84","scheduler":false,"jobs":[{"id":"scheduler","schedule":"* * * * *","command":"true","enabled":true}]}' jobs
# Worker fields: ids, counts, memory, the queue options, and what belongs to
# one kind only.
for w in '"id":"Queue","kind":"laravel-queue","processes":1' '"id":"1q","kind":"laravel-queue","processes":1' '"id":"a/b","kind":"laravel-queue","processes":1' \
	'"id":"abcdefghijklmnopq","kind":"laravel-queue","processes":1' '"id":"q","kind":"laravel-queue","processes":0' '"id":"q","kind":"laravel-queue","processes":11' \
	'"id":"q","kind":"laravel-queue","processes":1,"memory_mb":63' '"id":"q","kind":"laravel-queue","processes":1,"max_time":30' \
	'"id":"q","kind":"laravel-queue","processes":1,"connection":"Redis;id"' '"id":"q","kind":"laravel-queue","processes":1,"queues":["a b"]' \
	'"id":"q","kind":"laravel-queue","processes":1,"command":"id"' '"id":"q","kind":"custom","processes":1,"command":"id","queues":["x"]' \
	'"id":"q","kind":"shell","processes":1,"command":"id"'; do
	send_field "site.workers with {$w}" site.workers "{\"name\":\"php84\",\"workers\":[{$w}]}" workers
done
send_field "site.workers with the same worker twice" site.workers \
	'{"name":"php84","workers":[{"id":"q","kind":"laravel-queue","processes":1},{"id":"q","kind":"laravel-queue","processes":1}]}' workers
send_field "site.workers with no list" site.workers '{"name":"php84","workers":null}' workers
send_field "site.workers of the site ../php84" site.workers '{"name":"../php84","workers":[]}' name
send_field "site.cron of the site ../php84" site.cron '{"name":"../php84","scheduler":false,"jobs":[]}' name
for command in site.workers.status site.workers.restart; do
	send_field "$command of the site ../php84" "$command" '{"name":"../php84"}' name
done
send_field "site.workers.restart of the worker '../q'" site.workers.restart '{"name":"php84","worker":"../q"}' worker
for m in '"22"' '"26; rm -rf /"' '"../26"'; do
	send_field "node.install of the major $m" node.install "{\"major\":$m}" major
done
send_field "node.install of the major 26, a number" node.install '{"major":26}' params
# v0.5.0's backup commands (R05-D2, R05-D6): a destination's endpoint must
# be https:// and a host name alone, its prefix and path plain segments; a
# backup id is a UTC time, never a path.
k43=$(printf 'k%.0s' {1..43})
s43=$(printf 's%.0s' {1..43})
s3() {
	echo "{\"id\":\"vprobe\",\"type\":\"s3\",\"s3\":{\"endpoint\":\"$1\",\"region\":\"us-east-1\",\"bucket\":\"b-1\",\"prefix\":\"$2\",\"access_key_id\":\"a\",\"secret_access_key\":\"s\"},\"encryption_password\":\"$k43\",\"encryption_salt\":\"$s43\"}"
}
for endpoint in 'http://s3.example.com' 'https://s3.example.com/path' 'https://user@s3.example.com' 'https://169.254.169.254' 'https://S3.example.com'; do
	send_field "backup.destination with the endpoint $endpoint" backup.destination "$(s3 "$endpoint" one)" s3.endpoint
done
for prefix in '..' 'a/../b' '/abs' 'a//b' 'a/b/' 'A'; do
	send_field "backup.destination with the prefix $prefix" backup.destination "$(s3 https://s3.example.com "$prefix")" s3.prefix
done
send_field "backup.destination over SFTP into ../x" backup.destination \
	"{\"id\":\"vprobe\",\"type\":\"sftp\",\"sftp\":{\"host\":\"backup.example.com\",\"port\":22,\"user\":\"zimam\",\"path\":\"../x\"},\"encryption_password\":\"$k43\",\"encryption_salt\":\"$s43\"}" sftp.path
send_field "backup.destination with a key in MD5 form" backup.destination \
	"{\"id\":\"vprobe\",\"type\":\"sftp\",\"sftp\":{\"host\":\"backup.example.com\",\"port\":22,\"user\":\"zimam\",\"host_key_fingerprint\":\"MD5:00:11\"},\"encryption_password\":\"$k43\",\"encryption_salt\":\"$s43\"}" sftp.host_key_fingerprint
for backup in '../../x' '20261123T020000Z/../../x' '20261123T020000' ''; do
	send_field "backup.restore of the backup '$backup'" backup.restore "{\"plan\":\"nightly\",\"backup\":\"$backup\",\"part\":\"files-php84\",\"site\":\"php84\"}" backup
done
send_field "backup.restore of the part files-../php84" backup.restore '{"plan":"nightly","backup":"20261123T020000Z","part":"files-../php84","site":"php84"}' part
send_field "backup.plan with the schedule daily 25:00" backup.plan '{"id":"vprobe","destination":"s3","sites":["php84"],"schedule":"daily 25:00","retention":{"keep_last":1}}' schedule
send_field "backup.run of the plan ../x" backup.run '{"plan":"../x"}' plan
# v0.6.0's server security commands: reboot times, update scopes, the
# agent's own firewall rules, root login over SSH, and sources that aren't
# canonical (host bits, a zone, an IPv4-mapped IPv6 address). The guarded
# commands refuse before any change starts.
for at in '25:00' '03:00; id' '3:00' '+30' 'tomorrow'; do
	send_field "system.reboot at '$at'" system.reboot "{\"at\":\"$at\"}" at
done
send_field "updates.install with the scope dist" updates.install '{"scope":"dist"}' scope
send_field "updates.auto rebooting without a time" updates.auto '{"enabled":true,"auto_reboot":true}' reboot_time
rule() { echo "{\"rules\":[{\"id\":\"$1\",\"action\":\"allow\",\"proto\":\"tcp\",\"port_from\":8080,\"port_to\":8080,\"from\":$2}]}"; }
for id in ssh-22 http https; do
	send_field "firewall.rules setting the agent's rule $id" firewall.rules "$(rule "$id" '[]')" rules.0.id
done
for src in '10.0.0.1/8' 'fe80::1%eth0' '::ffff:192.0.2.1'; do
	send_field "firewall.rules from $src" firewall.rules "$(rule vprobe "[\"$src\"]")" rules.0.from
done
send_field "ssh.settings with root login yes" ssh.settings '{"password_auth":false,"root_login":"yes","ports":[22],"max_auth_tries":6,"login_grace_seconds":120}' root_login
send_field "cloudflare.mode restricting without the mode" cloudflare.mode '{"enabled":false,"restrict_origin":true}' restrict_origin
send_field "fail2ban.unban of a prefix" fail2ban.unban '{"jail":"sshd","address":"192.0.2.0/24"}' address
exit "$failed"
