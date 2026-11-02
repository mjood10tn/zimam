#!/usr/bin/env bash
# Done-when 1 (design §2): the installer exits 0, and its last line is the
# setup link. Keeps the link for the browser tests.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

status=$(cat "$E2E_WORK/install.status")
[[ $status == 0 ]] || die "FAIL done-when 1: the installer exited $status"
last=$(tail -n 1 "$E2E_WORK/install.out")
[[ $last =~ ^https://panel\.zimam\.test/setup/[A-Za-z0-9_-]{43}$ ]] ||
	die "FAIL done-when 1: the installer's last line isn't a setup link: $last"
printf '%s\n' "$last" >"$E2E_WORK/setup-link"
echo "PASS  done-when 1: the installer exited 0 and printed a setup link last"
