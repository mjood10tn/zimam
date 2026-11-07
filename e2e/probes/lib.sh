# shellcheck shell=bash disable=SC2034 # the probes that source this use its settings
# Sourced by the probes (design §14.4): sending raw lines to the agent with
# probeclient in the container, and reading the audit log's new entries.
# shellcheck source=../scripts/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../scripts/common.sh"

PROBECLIENT=/usr/local/bin/probeclient
SOCKET=/run/zimam/agent.sock
# A ULID, as request and job ids must be.
ULID=01JBX7Q3A1B2C3D4E5F6G7H8J9
failed=0

pass() { echo "PASS  $*"; }
fail() {
	echo "FAIL  $*"
	failed=1
}

audit_lines() {
	in_c bash -c 'wc -l </var/log/zimam/audit.log'
}

# audit_since prints the entries after the first $1 lines.
audit_since() {
	in_c tail -n "+$(($1 + 1))" /var/log/zimam/audit.log
}

# field reads one top-level field of the JSON object on stdin.
field() {
	python3 -I -c 'import json, sys; v = json.load(sys.stdin).get(sys.argv[1]); print("" if v is None else v)' "$1"
}

# The envelope fields every probe request shares.
env_ok="\"v\":1,\"id\":\"$ULID\",\"actor\":\"probe\""

# request <command> <params> sends one request as zimam and prints the reply.
request() {
	printf '%s\n' "{$env_ok,\"command\":\"$1\",\"params\":$2}" | bash "$E2E_ROOT/e2e/scripts/as.sh" zimam "$PROBECLIENT" "$SOCKET"
}

# has_field <prefix> reads an error reply on stdin and succeeds when one of
# its fields starts with the prefix.
has_field() {
	python3 -I -c 'import json, sys; f = json.load(sys.stdin).get("fields") or {}; sys.exit(0 if any(k.startswith(sys.argv[1]) for k in f) else 1)' "$1"
}

# probe <name> <code> <request> [field] sends the request as one line. With
# a field, the reply must name it (or a field under it) as the bad one.
probe() {
	local name=$1 code=$2 req=$3 want=${4:-} before reply entries
	before=$(audit_lines)
	reply=$(printf '%s\n' "$req" | bash "$E2E_ROOT/e2e/scripts/as.sh" zimam "$PROBECLIENT" "$SOCKET") || {
		fail "$name: probeclient failed"
		return
	}
	if [[ $(wc -l <<<"$reply") == 1 && $(field type <<<"$reply") == error && $(field code <<<"$reply") == "$code" ]] &&
		{ [[ -z $want ]] || has_field "$want" <<<"$reply"; }; then
		pass "$name answers $code${want:+ for $want}"
	else
		fail "$name answers ${reply:0:300}"
	fi
	entries=$(audit_since "$before")
	if [[ $(wc -l <<<"$entries") == 1 && $(field outcome <<<"$entries") == "error:$code" ]]; then
		pass "$name leaves one error:$code entry"
	else
		fail "$name leaves: ${entries:0:500}"
	fi
}
