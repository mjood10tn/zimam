#!/usr/bin/env bash
# Journal paging against the real journalctl (design §11.2), as zimam:
# nginx.service's newest 3 entries, the 3 before them by back_cursor, and
# forward again from the older page's forward_cursor, must line up with
# one read of the newest 6 — no entry lost or repeated at a page's edge.
# Task 35 checked these journalctl arguments only against a fake. Nginx is
# quiet while the probes run, so its journal doesn't move under the reads.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

# page <params> prints one cursor per entry, oldest first, then the reply's
# back and forward cursors as "back <c>" and "forward <c>".
page() {
	request logs.read "$1" | python3 -I -c '
import json, sys
r = json.load(sys.stdin)
assert r["type"] == "result" and r["ok"], r
d = r["data"]
for e in d["entries"]:
    print(e["cursor"])
print("back", d["back_cursor"])
print("forward", d["forward_cursor"])'
}

cursors() { grep -v '^back \|^forward ' <<<"$1"; }
cursor_of() { sed -n "s/^$1 //p" <<<"$2"; }

src='"source":"journal.nginx","direction":"back"'
all=$(page "{$src,\"limit\":6}") || die "logs.read of the journal: $all"
(($(cursors "$all" | wc -l) == 6)) || die "nginx.service has fewer than 6 journal entries: $all"
newest=$(page "{$src,\"limit\":3}")
older=$(page "{$src,\"limit\":3,\"cursor\":\"$(cursor_of back "$newest")\"}")
forward=$(page "{\"source\":\"journal.nginx\",\"direction\":\"forward\",\"limit\":3,\"cursor\":\"$(cursor_of forward "$older")\"}")

if [[ $(cursors "$older"; cursors "$newest") == "$(cursors "$all")" ]]; then
	pass "journal paging: the 3 newest entries, then the 3 before them by back_cursor, are the newest 6"
else
	fail "journal paging back: the pages don't make the newest 6"
	printf 'all:\n%s\nolder:\n%s\nnewest:\n%s\n' "$all" "$older" "$newest"
fi
if [[ $(cursors "$forward") == "$(cursors "$newest")" ]]; then
	pass "journal paging: forward from the older page's last entry gives the newest 3 again"
else
	fail "journal paging forward: got other entries"
	printf 'forward:\n%s\nnewest:\n%s\n' "$forward" "$newest"
fi
exit "$failed"
