#!/usr/bin/env bash
# Picks up, in the job that builds and tests (vps.yml), the server create.sh
# made in an earlier job: $E2E_WORK/access/ holds what create.sh left there
# (the artifact vps-access-<arch>). Tightens the key's mode, puts
# known_hosts, staging-roots.pem and state.env where the e2e scripts look,
# and adds CLIENT to state.env, this host's address as the server sees it,
# since this job runs on another runner than create.sh. Prints vps.env for
# GITHUB_ENV. Needs no token.
set -Eeuo pipefail

: "${E2E_WORK:?}"
here=$(cd "$(dirname "$0")" && pwd)
access=$E2E_WORK/access
die() {
	printf 'e2e: %s\n' "$*" >&2
	exit 1
}

chmod 0700 "$access" "$access/ssh"
chmod 0600 "$access/ssh/id_ed25519"
cp "$access/known_hosts" "$access/staging-roots.pem" "$E2E_WORK/"
set -a
# shellcheck source=/dev/null
source "$access/vps.env"
set +a
# shellcheck disable=SC2016 # expanded on the server
client=$(E2E_WORK=$E2E_WORK "$here/lxc-ssh" exec c -- bash -c 'echo ${SSH_CLIENT%% *}')
[[ -n $client ]] || die "the server didn't report this host's address"
{
	cat "$access/state.env"
	printf 'CLIENT=%s\n' "$client"
} >"$E2E_WORK/state.env"
printf '== this host is %s to the server\n' "$client" >&2
cat "$access/vps.env"
