#!/usr/bin/env bash
# The job-limit probe (design §5.7, §14.4): e2e.memhog, built into e2e
# agents alone, touches 512 MiB under its unit's MemoryMax=64M and
# MemorySwapMax=0. The job must end failed with systemd's reason oom-kill,
# and its failed unit must be reset once the job records it. Task 2 found
# MemoryMax enforced inside LXD once swap is off for the unit, so this runs
# on every e2e run. It writes only under /var/lib/zimam/jobs,
# /var/log/zimam/jobs and /run, so run-all.sh runs it between its snapshots.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

job=$(submit e2e.memhog '{}') || die "e2e.memhog wasn't accepted"
pass "e2e.memhog accepted as job $job"
status=$(job_end "$job" 90)
if [[ $status == "failed oom-kill" ]]; then
	pass "e2e.memhog ended failed with the reason oom-kill"
else
	fail "e2e.memhog ended '$status', want 'failed oom-kill'"
	in_c tail -n 5 "/var/log/zimam/jobs/$job.log" || true
fi

unit=zimam-job-$job.service
state=$(in_c systemctl show -p LoadState -p ActiveState --value "$unit" | paste -sd ' ')
if [[ $state != *failed* ]]; then
	pass "its unit isn't left failed ($state)"
else
	fail "its unit is still failed: $state"
fi
exit "$failed"
