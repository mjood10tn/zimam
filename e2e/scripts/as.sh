#!/usr/bin/env bash
# Runs a command in the container as a user, with stdin passed through:
#   as.sh <user> <command> [args...]
# AS_CWD sets its working directory (default /).
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"

(($# >= 2)) || die "usage: as.sh <user> <command> [args...]"
user=$1
shift
if [[ $user == root ]]; then
	lxc exec "$E2E_CONTAINER" --cwd "${AS_CWD:-/}" -- "$@"
else
	lxc exec "$E2E_CONTAINER" --cwd "${AS_CWD:-/}" -- runuser -u "$user" -- "$@"
fi
