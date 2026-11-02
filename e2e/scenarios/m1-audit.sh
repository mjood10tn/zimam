#!/usr/bin/env bash
# Done-when 11 for milestone 1 (design §2, §5.8): after the run, the audit
# log holds a cli:root entry for every bootstrap step and for each
# `panel setup-link`, the probes' rejections, and the contract suite's
# e2e.sleep submissions, outcome and cancels. It holds no successful
# routine read, and never a setup link.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

lxc file pull -q "$E2E_CONTAINER/var/log/zimam/audit.log" "$E2E_WORK/audit.log"
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

check(count(command="e2e.sleep", outcome="accepted") >= 3, "e2e.sleep submissions accepted")
check(count(command="e2e.sleep", outcome="ok") >= 1, "an e2e.sleep job's outcome")
check(count(command="e2e.sleep", outcome="error:busy") >= 1, "e2e.sleep refused as busy")
check(count(command="job.cancel", outcome="ok") >= 2, "job.cancel audited")

reads = {"agent.version", "system.status", "system.metrics", "job.status", "job.follow", "logs.list", "logs.read"}
bad = [e for e in entries if e["command"] in reads and not e["outcome"].startswith("error:")]
check(not bad, f"no routine read audited ({len(bad)} found)")
sys.exit(1 if failed else 0)
PY
