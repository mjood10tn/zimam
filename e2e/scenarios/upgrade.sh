#!/usr/bin/env bash
# The upgrade (design §4, D25): a server installed from the previous
# release, set up and with sites, takes this tree's release when the
# installer runs again. On a fresh container or server, after up.sh,
# build-previous.sh, and build.sh with E2E_RELEASE_KEY and a higher
# E2E_VERSION (README.md):
#   1. serve the previous release and install it with curl | bash;
#   2. complete setup through the setup link, in Chromium (the setup
#      project);
#   3. provision up-static and up-php (on the default PHP) through the
#      socket, and check both answer;
#   4. serve this tree's release and run the installer again. Then the agent
#      reports the new version, bootstrap installed the new panel release,
#      both sites still answer 200 with their placeholder, a new static
#      site up-new provisions and answers 200, the panel's /admin/login
#      answers 200, maintenance exits 0, and the audit log has
#      the installer's cli:root entries from both runs;
#   5. serve the previous release again and run its installer: D25 refuses
#      the downgrade and changes nothing.
# shellcheck source=../probes/lib.sh
source "$(dirname "$0")/../probes/lib.sh"
load_state

prev=$E2E_WORK/release-previous
agent=/usr/local/sbin/zimam-agent
manifest_version() {
	python3 -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$1"
}
old=$(manifest_version "$prev/dist/manifest.json") || die "no previous release in $prev: run e2e/scripts/build-previous.sh first"
new=$(manifest_version "$E2E_WORK/dist/manifest.json") || die "no release in $E2E_WORK/dist: run e2e/scripts/build.sh first"
log "from $old to $new"

# installer <name> runs the installer the release server serves, as §4
# tells an admin to, keeping its output as install.<name>.out and .err
# (collect-logs.sh takes install.*), and prints its exit code.
installer() {
	local cmd code=0
	cmd="curl -fsSL http://$BRIDGE:$E2E_PORT/latest/download/install.sh | bash -s -- --panel-domain $E2E_PANEL"
	cmd+=" $E2E_INSTALL_FLAGS --yes"
	log "$cmd"
	in_c bash -c "$cmd" >"$E2E_WORK/install.$1.out" 2>"$E2E_WORK/install.$1.err" || code=$?
	cat "$E2E_WORK/install.$1.err" >&2
	echo "$code"
}

# site_status <name> prints the status code of the site's / over HTTP and
# keeps the body in site.body.
site_status() {
	curl -sS -m 10 -o "$E2E_WORK/site.body" -w '%{http_code}' --resolve "$1.$E2E_DOMAIN:80:$CIP" "http://$1.$E2E_DOMAIN/"
}

# check_sites <when> checks that both sites answer 200 with their placeholder.
check_sites() {
	local code body
	code=$(site_status up-static) || code=none
	body=$(cat "$E2E_WORK/site.body" 2>/dev/null || true)
	if [[ $code == 200 && $body == *"Zimam site up-static is ready."* ]]; then
		pass "$1: up-static answers 200 with its placeholder"
	else
		fail "$1: up-static answered $code: ${body:0:300}"
	fi
	code=$(site_status up-php) || code=none
	body=$(cat "$E2E_WORK/site.body" 2>/dev/null || true)
	if [[ $code == 200 && $body == "Zimam site up-php is ready. PHP $php."* ]]; then
		pass "$1: up-php answers 200 with its placeholder, PHP ${body##* PHP }"
	else
		fail "$1: up-php answered $code: ${body:0:300}"
	fi
}

# bootstrap_steps <from> <to> prints the bootstrap steps audited as cli:root
# with outcome ok in the audit log's lines from+1 to to, sorted.
bootstrap_steps() {
	in_c sed -n "$(($1 + 1)),$2p" /var/log/zimam/audit.log | python3 -I -c '
import json, sys
steps = set()
for line in sys.stdin:
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("actor") == "cli:root" and e.get("command") == "bootstrap" and e.get("outcome") == "ok":
        steps.add((e.get("params") or {}).get("step"))
print(" ".join(sorted(s for s in steps if s)))'
}

# has_steps <steps> <want...> succeeds when every wanted step is in steps.
has_steps() {
	local steps=" $1 " step
	shift
	for step in "$@"; do
		[[ $steps == *" $step "* ]] || return 1
	done
}

# 1. The previous release.
E2E_DIST=$prev/dist bash "$E2E_ROOT/e2e/scripts/serve-release.sh"
bash "$E2E_ROOT/e2e/scripts/install.sh" || die "FAIL previous: the installer for $old exited $(cat "$E2E_WORK/install.status")"
last=$(tail -n 1 "$E2E_WORK/install.out")
[[ $last =~ $E2E_SETUP_LINK ]] || die "FAIL previous: the installer's last line isn't a setup link: $last"
printf '%s\n' "$last" >"$E2E_WORK/setup-link"
installed=$(in_c "$agent" version)
[[ $installed == "$old" ]] || die "FAIL previous: the installed agent reports $installed, not $old"
first=$(audit_lines)
echo "PASS  previous: release $old installed and printed a setup link"

# 2. Setup, through the link.
bash "$E2E_ROOT/e2e/scripts/browser.sh" --project setup || die "FAIL setup: the setup project failed on $old"
echo "PASS  setup: the admin set up through the link, with 2FA, on $old"

# 3. Two sites, through the socket.
lxc file push -q --mode 0755 "$E2E_WORK/bin/probeclient" "$E2E_CONTAINER$PROBECLIENT"
php=$(request system.status '{}' | python3 -I -c 'import json, sys; print(json.load(sys.stdin)["data"]["php"]["default"])')
[[ $php =~ ^[0-9]+\.[0-9]+$ ]] || die "FAIL sites: system.status names the default PHP '$php'"
run_job site.provision "{\"name\":\"up-static\",\"domain\":\"up-static.$E2E_DOMAIN\",\"type\":\"static\"}"
run_job site.provision "{\"name\":\"up-php\",\"domain\":\"up-php.$E2E_DOMAIN\",\"type\":\"php\",\"php_version\":\"$php\"}"
check_sites "on $old"
((failed == 0)) || die "FAIL sites: the sites don't answer on $old, before any upgrade"

# 4. The upgrade: this tree's release, through the installer again.
mark=$(audit_lines)
bash "$E2E_ROOT/e2e/scripts/serve-release.sh"
code=$(installer upgrade)
[[ $code == 0 ]] || die "FAIL upgrade: the installer for $new exited $code on $old"
last=$(tail -n 1 "$E2E_WORK/install.upgrade.out")
[[ $last =~ $E2E_SETUP_LINK ]] || die "FAIL upgrade: the installer's last line isn't a setup link: $last"
pass "upgrade: the installer for $new ran on $old and printed a setup link"
installed=$(in_c "$agent" version)
if [[ $installed == "$new" ]]; then
	pass "upgrade: zimam-agent version reports $new"
else
	fail "upgrade: zimam-agent version reports $installed, not $new"
fi
current=$(in_c readlink /home/zimam/panel/current) || current=none
if [[ $current == "releases/$new" ]]; then
	pass "upgrade: the panel's current release is $new"
else
	fail "upgrade: the panel's current points at $current, not releases/$new"
fi
check_sites "on $new"
# A new site on the upgraded server. A release before v0.1.1 leaves no
# uid-highwater.json, so the new site user's uid then comes from
# /etc/passwd alone, above the sites provisioned on $old.
mark_file=without
in_c test -e /var/lib/zimam/uid-highwater.json && mark_file=with
if job=$(submit site.provision "{\"name\":\"up-new\",\"domain\":\"up-new.$E2E_DOMAIN\",\"type\":\"static\"}"); then
	status=$(job_end "$job")
else
	status="not accepted"
fi
# The job ends right after nginx reloads, and a request then can reach a
# worker that is shutting down (curl: Empty reply from server), so the
# site gets a few seconds to answer.
rm -f "$E2E_WORK/site.body"
for _ in $(seq 10); do
	code=$(site_status up-new) || code=none
	[[ $code == none ]] || break
	sleep 2
done
body=$(cat "$E2E_WORK/site.body" 2>/dev/null || true)
if [[ $status == succeeded* && $code == 200 && $body == *"Zimam site up-new is ready."* ]]; then
	pass "upgrade: up-new, provisioned on $new $mark_file uid-highwater.json, answers 200 with its placeholder"
else
	fail "upgrade: provisioning up-new on $new ended $status, and it answered $code: ${body:0:300}"
fi
code=$(curl -sS -m 10 -o /dev/null -w '%{http_code}' --cacert "$E2E_CA" --resolve "$E2E_PANEL:443:$CIP" "https://$E2E_PANEL/admin/login") || code=none
if [[ $code == 200 ]]; then
	pass "upgrade: the panel's /admin/login answers 200"
else
	fail "upgrade: the panel's /admin/login answered $code"
fi
if in_c "$agent" maintenance >"$E2E_WORK/maintenance.txt" 2>&1; then
	pass "upgrade: zimam-agent maintenance exits 0"
else
	fail "upgrade: zimam-agent maintenance failed: $(tail -n 20 "$E2E_WORK/maintenance.txt")"
fi
end=$(audit_lines)
before=$(bootstrap_steps 0 "$first")
after=$(bootstrap_steps "$mark" "$end")
if has_steps "$before" panel-release setup-link; then
	pass "audit: the install of $old left cli:root bootstrap entries: $before"
else
	fail "audit: the install of $old left cli:root bootstrap entries for '$before', want panel-release and setup-link"
fi
if has_steps "$after" panel-release setup-link; then
	pass "audit: the upgrade to $new left cli:root bootstrap entries: $after"
else
	fail "audit: the upgrade to $new left cli:root bootstrap entries for '$after', want panel-release and setup-link"
fi
errors=$(in_c sed -n "$((mark + 1)),${end}p" /var/log/zimam/audit.log | python3 -I -c '
import json, sys
n = 0
for line in sys.stdin:
    try:
        e = json.loads(line)
    except ValueError:
        n += 1
        continue
    n += e.get("actor") == "cli:root" and e.get("outcome") != "ok"
print(n)') || errors="unreadable"
if ((end <= mark)); then
	fail "audit: the upgrade added no audit entry (lines $mark to $end)"
elif [[ $errors == 0 ]]; then
	pass "audit: no cli:root entry of the upgrade failed"
else
	fail "audit: $errors cli:root entries of the upgrade failed: $(audit_since "$mark")"
fi

# 5. The previous release again: D25 refuses to move the server back.
mark=$(audit_lines)
E2E_DIST=$prev/dist bash "$E2E_ROOT/e2e/scripts/serve-release.sh"
code=$(installer downgrade)
installed=$(in_c "$agent" version)
if [[ $code != 0 ]] && grep -q "runs zimam-agent $new, which is newer than release $old" "$E2E_WORK/install.downgrade.err" &&
	[[ $installed == "$new" && $(audit_lines) == "$mark" ]]; then
	pass "refuses a downgrade: the installer for $old exited $code, and $new stays installed"
else
	fail "refuses a downgrade: the installer for $old exited $code; the agent reports $installed; the audit log grew by $(($(audit_lines) - mark)) lines"
fi
check_sites "after the refused downgrade"
exit "$failed"
