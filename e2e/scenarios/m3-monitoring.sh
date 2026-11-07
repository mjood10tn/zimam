#!/usr/bin/env bash
# Done-when 8 (design §2, §7.5), outside the browser:
#   m3-monitoring.sh samples   the first metric_samples row came within 2
#                              minutes of the install's last bootstrap step,
#                              and the scheduler still samples every minute
#   m3-monitoring.sh prune     a sample dated 8 days ago, added with tinker
#                              as zimam, goes with zimam:prune-metrics; the
#                              recent ones stay
# The browser checks that the Monitoring page shows live values.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

case ${1:-} in
samples)
	in_c cat /var/log/zimam/audit.log >"$E2E_WORK/audit-m3.log"
	rows=$(panel_sql 'select min(sampled_at) as first, max(sampled_at) as last, count(*) as n from metric_samples')
	python3 -I - "$E2E_WORK/audit-m3.log" "$rows" <<'PY' || die "FAIL done-when 8: see above"
import json, sys
from datetime import datetime, timezone

installed = max(
    datetime.fromisoformat(e["ts"].replace("Z", "+00:00"))
    for e in map(json.loads, open(sys.argv[1]))
    if e["command"] == "bootstrap" and (e.get("params") or {}).get("step") == "setup-link"
)
row = json.loads(sys.argv[2])[0]
if not row["n"]:
    print("FAIL  done-when 8: no metric_samples row")
    sys.exit(1)
# The panel stores UTC (config/app.php).
at = lambda s: datetime.strptime(s, "%Y-%m-%d %H:%M:%S").replace(tzinfo=timezone.utc)
first, last = at(row["first"]), at(row["last"])
after = (first - installed).total_seconds()
age = (datetime.now(timezone.utc) - last).total_seconds()
ok = after <= 120 and age <= 120 and row["n"] >= 2
print(("PASS  " if ok else "FAIL  ") + f"done-when 8: the first sample came {after:.0f} s after the install's last step "
      f"(want at most 120), {row['n']} samples, the newest {age:.0f} s old (want at most 120)")
sys.exit(0 if ok else 1)
PY
	;;
prune)
	# A copy of the newest sample, dated 8 days ago.
	# shellcheck disable=SC2016 # PHP's variables
	out=$(artisan tinker --execute '$s = App\Models\MetricSample::query()->latest("sampled_at")->first()->replicate(); $s->sampled_at = now()->subDays(8); $s->save(); echo "OLD=".$s->id."\n";')
	old=$(sed -n 's/^OLD=\([0-9]*\)$/\1/p' <<<"$out")
	[[ -n $old ]] || die "FAIL done-when 8: tinker added no sample: $out"
	recent=$(panel_sql "select id from metric_samples where sampled_at >= datetime('now', '-7 days') order by id")
	n=$(python3 -I -c 'import json, sys; print(len(json.loads(sys.argv[1])))' "$recent")
	((n >= 2)) || die "FAIL done-when 8: only $n recent samples"
	artisan zimam:prune-metrics
	left=$(panel_sql 'select id from metric_samples where id = ?' "$old")
	[[ $left == '[]' ]] || die "FAIL done-when 8: the 8-day-old sample $old is still there"
	missing=$(python3 -I -c '
import json, sys
before = {r["id"] for r in json.loads(sys.argv[1])}
after = {r["id"] for r in json.loads(sys.argv[2])}
print(sorted(before - after))' "$recent" "$(panel_sql 'select id from metric_samples')")
	[[ $missing == '[]' ]] || die "FAIL done-when 8: zimam:prune-metrics deleted recent samples $missing"
	echo "PASS  done-when 8: zimam:prune-metrics deleted the 8-day-old sample and kept all $n recent ones"
	;;
*) die "usage: m3-monitoring.sh samples|prune" ;;
esac
