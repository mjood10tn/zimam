#!/usr/bin/env bash
# The log archive probes (design §11.2, §14.4), as zimam: logs.read takes
# an archive only by a name from logs.list's own listing. Paths and
# traversal are refused as validation_failed, with one audit entry each; a
# well-formed name that isn't in php84's listing is not_found, and reads
# nothing. The archive milestone 3's rotation made is read, to show the
# refusals aren't all a read can do. run-all.sh runs these between its two
# file snapshots.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

# read_params <archive> asks for php84's access log archive, back from its end.
read_params() {
	echo "{\"source\":\"site.nginx-access\",\"name\":\"php84\",\"archive\":\"$1\",\"direction\":\"back\",\"limit\":5}"
}

for name in ../../../../etc/shadow /etc/shadow access.log-1-1.gz/../../x ../access.log-20261101-1.gz \
	'access.log-20261101-1.gz\u0000' 'access.log-20261101-1.gz\n' .access.log-20261101-1.gz; do
	probe "logs.read of the archive $name" validation_failed "{$env_ok,\"command\":\"logs.read\",\"params\":$(read_params "$name")}" archive
done

listing=$(request logs.list '{"source":"site.nginx-access","name":"php84"}')
names=$(python3 -I -c 'import json, sys; r = json.load(sys.stdin); assert r["type"] == "result", r; print(*[a["name"] for a in r["data"]["archives"]], sep="\n")' <<<"$listing") ||
	die "logs.list of php84's access log: $listing"
listed=$(head -n 1 <<<"$names")
[[ -n $listed ]] || die "php84's access log has no archive: milestone 3's rotation makes one"

# A well-formed name that isn't listed: not_found, and no audit entry, as
# a read's own failure isn't one (§5.8).
unlisted=access.log-20200101-1577836800.gz
grep -qxF "$unlisted" <<<"$names" && die "$unlisted is listed"
before=$(audit_lines)
reply=$(request logs.read "$(read_params "$unlisted")")
if [[ $(field type <<<"$reply") == error && $(field code <<<"$reply") == not_found ]]; then
	pass "logs.read of the unlisted archive $unlisted answers not_found"
else
	fail "logs.read of the unlisted archive $unlisted answers ${reply:0:300}"
fi
[[ $(audit_lines) == "$before" ]] || fail "the not_found read left an audit entry"

# The listed archive, read forward from its start: m3-logs.sh's request is
# its first line. (Its end is 201 MiB of zeros with no newline, which a read
# back from the end leaves out as an unfinished line.)
reply=$(request logs.read "{\"source\":\"site.nginx-access\",\"name\":\"php84\",\"archive\":\"$listed\",\"direction\":\"forward\",\"offset\":0,\"limit\":5}")
if python3 -I -c '
import json, sys
r = json.load(sys.stdin)
assert r["type"] == "result" and r["ok"], r
assert any("/e2e-archive-probe-2 " in l["text"] for l in r["data"]["lines"]), r["data"]["lines"]' <<<"$reply"; then
	pass "logs.read of the listed archive $listed finds the request logged before it rotated"
else
	fail "logs.read of the listed archive $listed answers ${reply:0:300}"
fi
exit "$failed"
