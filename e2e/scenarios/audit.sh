#!/usr/bin/env bash
# Done-when 11 (design §2, §5.8): the audit re-check. First every one of
# §5.3's 18 commands runs at least once: the scenarios and probes before
# this sent all but php.uninstall, which runs here on PHP 8.3 (installed
# for it and removed again), and the seven reads run here once each, as
# zimam, and must add no entry. Then certbot renews every certificate
# against Pebble, so `hook cert-deployed` runs once per lineage. Then the
# whole audit log: every audited command has its submissions and outcomes,
# every accepted job has exactly one outcome, password hashes show as
# "[redacted]", no successful read is in it, and every state-changing
# command-line run is there as cli:root: each bootstrap step, each
# `panel setup-link`, the hook, and maintenance only when it changed
# something. Also the probes' rejections and the counts the milestone
# scenarios leave, and never a setup link.
# shellcheck source=../probes/lib.sh
source "$(dirname "$0")/../probes/lib.sh"

mark=$(audit_lines)

# job <command> <params> runs a job and prints its id; apt may take minutes.
job() {
	local id status
	id=$(submit "$1" "$2") || die "$1 wasn't accepted"
	status=$(job_end "$id" 900)
	[[ $status == succeeded* ]] || die "$1 $2 ended $status"
	echo "$id"
}
job php.install '{"version":"8.3"}' >/dev/null
uninstall=$(job php.uninstall '{"version":"8.3"}')
installed=$(request system.status '{}' | python3 -I -c 'import json, sys; print(" ".join(p["version"] for p in json.load(sys.stdin)["data"]["php"]["installed"]))')
if [[ " $installed " != *" 8.3 "* ]]; then
	pass "php.install and php.uninstall of 8.3 ran; installed now: $installed"
else
	fail "PHP 8.3 is still installed after php.uninstall: $installed"
fi

# The seven reads, each answered, none audited.
before=$(audit_lines)
reads=(
	'agent.version' '{}'
	'system.status' '{}'
	'system.metrics' '{}'
	'job.status' "{\"job\":\"$uninstall\"}"
	'job.follow' "{\"job\":\"$uninstall\",\"from_offset\":0}"
	'logs.list' '{"source":"site.nginx-access","name":"php84"}'
	'logs.read' '{"source":"journal.nginx","direction":"back","limit":3}'
)
for ((i = 0; i < ${#reads[@]}; i += 2)); do
	reply=$(request "${reads[i]}" "${reads[i + 1]}")
	# job.follow sends the job's log lines first; the result comes last.
	if python3 -I -c 'import json, sys; r = json.loads(sys.stdin.read().splitlines()[-1]); sys.exit(0 if r["type"] == "result" and r["ok"] else 1)' <<<"$reply" 2>/dev/null; then
		pass "${reads[i]} answered"
	else
		fail "${reads[i]} answered ${reply:0:300}"
	fi
done
if [[ $(audit_lines) == "$before" ]]; then
	pass "the seven reads added no audit entry"
else
	fail "the reads added: $(audit_since "$before")"
fi

# certbot's renewal confs name Pebble and the hook, but not Pebble's CA,
# which the agent passes to cert.issue's certbot the same way.
bundle=$(in_c cat /etc/zimam/agent.json | python3 -I -c 'import json, sys; print(json.load(sys.stdin).get("acme_ca_bundle") or "")')
lineages=$(in_c bash -c 'cd /etc/letsencrypt/renewal && ls zimam-*.conf' | sed 's/\.conf$//' | LC_ALL=C sort | tr '\n' ' ')
in_c env "REQUESTS_CA_BUNDLE=$bundle" certbot renew --force-renewal --no-random-sleep-on-renew >"$E2E_WORK/certbot-renew.txt" 2>&1 ||
	die "certbot renew failed: $(tail -n 20 "$E2E_WORK/certbot-renew.txt")"
hooked=$(audit_since "$mark" | python3 -I -c '
import json, sys
names = set()
for line in sys.stdin:
    e = json.loads(line)
    if e["actor"] == "cli:root" and e["command"] == "hook cert-deployed" and e["outcome"] == "ok":
        names.add(e["params"]["lineage"].rstrip("/").rsplit("/", 1)[-1])
print(" ".join(sorted(names)))')
if [[ "$hooked " == "$lineages" ]]; then
	pass "certbot renew ran hook cert-deployed for every lineage, audited as cli:root: $hooked"
else
	fail "hook cert-deployed audited for '$hooked', want '$lineages'"
fi

# Read through cat, so this user owns the copy: lxc runs as root here.
in_c cat /var/log/zimam/audit.log >"$E2E_WORK/audit.log"
python3 -I - "$E2E_WORK/audit.log" <<'PY' || failed=1
import json, sys

raw = open(sys.argv[1]).read()
entries = [json.loads(line) for line in raw.splitlines()]
failed = False


def check(ok, what):
    global failed
    print(("PASS  " if ok else "FAIL  ") + what)
    failed |= not ok


def count(**want):
    return sum(all(e.get(k) == v for k, v in want.items()) for e in entries)


# §5.3's 18 commands: the jobs are audited at submission and outcome,
# job.cancel when it acts, and the reads never, unless refused.
jobs = ["php.install", "php.uninstall", "php.default", "site.provision", "site.php", "site.security",
        "site.destroy", "cert.issue", "security.check", "logs.policy"]
reads = ["agent.version", "system.status", "system.metrics", "job.status", "job.follow", "logs.list", "logs.read"]
assert len(jobs) + 1 + len(reads) == 18
for command in jobs:
    for outcome in ("accepted", "ok"):
        n = count(command=command, outcome=outcome)
        check(n >= 1, f"{command} {outcome}: {n}")
check(count(command="job.cancel", outcome="ok") >= 1, "job.cancel audited")
bad = [e for e in entries if e["command"] in reads and not e["outcome"].startswith("error:")]
check(not bad, f"no successful read audited ({len(bad)} found)")

# Every accepted job has exactly one outcome entry, ok or error:<code>; a
# cancelled job's is the job.cancel that stopped it (jobs.auditOutcome).
outcomes = {}
for e in entries:
    if e.get("job") and e["outcome"] != "accepted" and (e["command"] != "job.cancel" or e["outcome"] == "ok"):
        outcomes.setdefault(e["job"], []).append(e)
accepted = [e for e in entries if e["outcome"] == "accepted"]
missing = [f'{e["command"]} {e["job"]}' for e in accepted
           if [o["command"] for o in outcomes.get(e["job"], [])] not in ([e["command"]], ["job.cancel"])]
check(accepted and not missing, f"each of {len(accepted)} accepted jobs has one outcome entry"
      + (f"; not: {missing[:10]}" if missing else ""))
ids = {e["job"] for e in accepted}
orphans = [j for j in outcomes if j not in ids]
check(not orphans, f"no outcome without its submission ({orphans[:10]})")

# Secrets: every password_hash, wherever it is, shows as "[redacted]".
hashes = []


def walk(v):
    if isinstance(v, dict):
        for k, x in v.items():
            if k == "password_hash":
                hashes.append(x)
            walk(x)
    elif isinstance(v, list):
        for x in v:
            walk(x)


for e in entries:
    walk(e.get("params"))
check(hashes and all(h == "[redacted]" for h in hashes),
      f"every password_hash shows as \"[redacted]\" ({len(hashes)}; others: {[h for h in hashes if h != '[redacted]'][:3]})")
check(not any(p in raw for p in ("$2y$", "$2a$", "$2b$")), "no bcrypt hash in the audit log")
check("/setup/" not in raw, "no setup link in the audit log")

# The command line, as cli:root.
steps = ["agent-config", "directories", "sury-repository", "php", "nginx", "panel-release",
         "panel-site", "panel-certificate", "units", "log-policy", "setup-link"]
for step in steps:
    n = sum(e["actor"] == "cli:root" and e["command"] == "bootstrap" and e["outcome"] == "ok"
            and (e.get("params") or {}).get("step") == step for e in entries)
    check(n >= 1, f"bootstrap step {step} audited as cli:root ({n})")
n = count(actor="cli:root", command="panel setup-link", outcome="ok")
check(n >= 2, f"each panel setup-link run audited as cli:root ({n})")
n = count(actor="cli:root", command="hook cert-deployed", outcome="ok")
check(n >= 1, f"hook cert-deployed audited as cli:root ({n})")
runs = [e for e in entries if e["command"] == "maintenance"]
idle = [e for e in runs if e["actor"] != "cli:root" or not any(
    (e.get("params") or {}).get(k) for k in ("restored_configs", "reconciled_jobs", "keyring_refreshed",
                                             "deleted_archives", "pruned_jobs"))]
check(runs and not idle, f"maintenance audited as cli:root only when it changed something ({len(runs)} runs, {len(idle)} not)")
n = sum(bool((e.get("params") or {}).get("deleted_archives")) for e in runs)
check(n >= 1, f"maintenance's deletion of the backdated archive audited ({n})")

# The probes' rejections.
n = sum(e["outcome"] == "error:forbidden" and e["peer_uid"] not in (None, 0) for e in entries)
check(n >= 2, f"the callers probes' rejections ({n} error:forbidden)")
for code, least in [("invalid_request", 5), ("unsupported_protocol", 1), ("unknown_command", 1), ("validation_failed", 2)]:
    n = count(outcome="error:" + code)
    check(n >= least, f"the protocol probes' rejections: {n} error:{code}, want at least {least}")
check(count(command="site.provision", outcome="error:busy") >= 1, "site.provision refused as busy")

# The milestone scenarios' counts: the contract suite's two sites, the
# three browser sites and probe-static; PHP 8.4 and its default; three
# certificates; php84 to 8.5 and back; probe-static's destroy; php84's
# seven security changes from the browser; the server's and three sites'
# checks; the log policy.
for command, least in [("site.provision", 6), ("php.install", 2), ("php.uninstall", 1), ("php.default", 1),
                       ("cert.issue", 3), ("site.php", 2), ("site.destroy", 1), ("site.security", 7),
                       ("security.check", 4), ("logs.policy", 1)]:
    n = count(command=command, outcome="accepted")
    check(n >= least, f"{command} accepted: {n}, want at least {least}")
for command, least in [("site.provision", 6), ("php.install", 2), ("php.uninstall", 1), ("php.default", 1),
                       ("cert.issue", 3), ("site.php", 2), ("site.destroy", 1), ("site.security", 6),
                       ("security.check", 4), ("logs.policy", 1)]:
    n = count(command=command, outcome="ok")
    check(n >= least, f"{command} ok: {n}, want at least {least}")
n = sum(e["command"] == "site.security" and e["outcome"].startswith("error:") and e["outcome"] != "error:validation_failed"
        and bool(e.get("job")) for e in entries)
check(n >= 1, f"the site.security job that met the broken tls.conf audited as failed ({n})")
check(count(command="e2e.sleep", outcome="accepted") >= 1, "the e2e.sleep submission accepted")
# The validation probes, and the contract suite's bad site name.
for command, least in [("site.provision", 23), ("php.install", 3), ("site.php", 3), ("site.destroy", 3), ("cert.issue", 3),
                       ("site.security", 31), ("logs.read", 7)]:
    n = count(command=command, outcome="error:validation_failed")
    check(n >= least, f"{command} refused as validation_failed: {n}, want at least {least}")
sys.exit(1 if failed else 0)
PY
exit "$failed"
