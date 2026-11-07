#!/usr/bin/env bash
# Done-when 11 (design §2, §5.8), for every command so far: after the run,
# the audit log holds a cli:root entry for every bootstrap step and for
# each `panel setup-link`, the probes' rejections, the contract suite's
# site.provision submissions, outcomes and refusals and its e2e.sleep
# submission and cancel, and milestone 2's php.install, php.default,
# site.provision, cert.issue, site.php and site.destroy submissions and
# outcomes, from the browser scenarios and the symlink probe, and
# milestone 3's site.security, security.check and logs.policy submissions
# and outcomes, its validation refusals and the retention's maintenance
# entry. It holds no successful routine read, and never a setup link.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

# Read through cat, so this user owns the copy: lxc runs as root here.
in_c cat /var/log/zimam/audit.log >"$E2E_WORK/audit.log"
python3 -I - "$E2E_WORK/audit.log" <<'PY'
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


steps = ["agent-config", "directories", "sury-repository", "php", "nginx", "panel-release",
         "panel-site", "panel-certificate", "units", "log-policy", "setup-link"]
for step in steps:
    n = sum(e["actor"] == "cli:root" and e["command"] == "bootstrap" and e["outcome"] == "ok"
            and (e.get("params") or {}).get("step") == step for e in entries)
    check(n >= 1, f"bootstrap step {step} audited as cli:root ({n})")
n = count(actor="cli:root", command="panel setup-link", outcome="ok")
check(n >= 2, f"each panel setup-link run audited as cli:root ({n})")
check("/setup/" not in raw, "no setup link in the audit log")

n = sum(e["outcome"] == "error:forbidden" and e["peer_uid"] not in (None, 0) for e in entries)
check(n >= 2, f"the callers probes' rejections ({n} error:forbidden)")
for code, least in [("invalid_request", 5), ("unsupported_protocol", 1), ("unknown_command", 1), ("validation_failed", 2)]:
    n = count(outcome="error:" + code)
    check(n >= least, f"the protocol probes' rejections: {n} error:{code}, want at least {least}")

check(count(command="site.provision", outcome="error:busy") >= 1, "site.provision refused as busy")

# Submissions and the jobs' outcomes: the contract suite's two sites, the
# three browser sites and probe-static; PHP 8.4 and its default; three
# certificates; php84 to 8.5 and back; probe-static's destroy.
for command, least in [("site.provision", 6), ("php.install", 1), ("php.default", 1), ("cert.issue", 3),
                       ("site.php", 2), ("site.destroy", 1)]:
    for outcome in ("accepted", "ok"):
        n = count(command=command, outcome=outcome)
        check(n >= least, f"{command} {outcome}: {n}, want at least {least}")

# The validation probes, and the contract suite's bad site name.
for command, least in [("site.provision", 23), ("php.install", 3), ("site.php", 3), ("site.destroy", 3), ("cert.issue", 3)]:
    n = count(command=command, outcome="error:validation_failed")
    check(n >= least, f"{command} refused as validation_failed: {n}, want at least {least}")
check(count(command="e2e.sleep", outcome="accepted") >= 1, "the e2e.sleep submission accepted")
check(count(command="job.cancel", outcome="ok") >= 1, "job.cancel audited")

# Milestone 3: php84's seven security changes from the browser, one of
# which met the broken tls.conf and failed; the server's and three sites'
# checks; the log policy; and the archive retention deleted.
for command, least in [("site.security", 7), ("security.check", 4), ("logs.policy", 1)]:
    n = count(command=command, outcome="accepted")
    check(n >= least, f"{command} accepted: {n}, want at least {least}")
for command, least in [("site.security", 6), ("security.check", 4), ("logs.policy", 1)]:
    n = count(command=command, outcome="ok")
    check(n >= least, f"{command} ok: {n}, want at least {least}")
n = sum(e["command"] == "site.security" and e["outcome"].startswith("error:") and e["outcome"] != "error:validation_failed"
        and bool(e.get("job")) for e in entries)
check(n >= 1, f"the site.security job that met the broken tls.conf audited as failed ({n})")
for command, least in [("site.security", 31), ("logs.read", 7)]:
    n = count(command=command, outcome="error:validation_failed")
    check(n >= least, f"{command} refused as validation_failed: {n}, want at least {least}")
n = sum(e["command"] == "maintenance" and bool((e.get("params") or {}).get("deleted_archives")) for e in entries)
check(n >= 1, f"maintenance's deletion of the backdated archive audited ({n})")

reads = {"agent.version", "system.status", "system.metrics", "job.status", "job.follow", "logs.list", "logs.read"}
bad = [e for e in entries if e["command"] in reads and not e["outcome"].startswith("error:")]
check(not bad, f"no routine read audited ({len(bad)} found)")
sys.exit(1 if failed else 0)
PY
