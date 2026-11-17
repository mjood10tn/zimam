#!/usr/bin/env bash
# Sourced by the scenario and the probe that use MySQL and Valkey in the
# server (scenarios/v03-databases.sh, probes/databases.sh): their clients, as
# the agent's root and as a site's user. A password is read from a file on
# this host and passed on stdin, so it is in no command line.
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

# mysql_root <sql> runs SQL as MySQL's root through the socket, the way the
# agent does: root of the server, authenticated by the OS.
mysql_root() {
	in_c mysql --protocol=socket --user=root --batch --skip-column-names --execute "$1"
}

# mysql_as <user> <password file> <socket|tcp> <sql> runs SQL as a site's
# user, over the socket or over TCP to 127.0.0.1, with client options after
# the SQL. stderr is stdout's too, so a refusal is in the output.
mysql_as() {
	local user=$1 pwfile=$2 protocol=$3 sql=$4 host=()
	shift 4
	[[ $protocol == socket ]] || host=(--host 127.0.0.1)
	# shellcheck disable=SC2016 # expanded by the inner bash
	in_c bash -c 'read -r MYSQL_PWD; export MYSQL_PWD; exec mysql --protocol="$1" --user="$2" --batch --skip-column-names --execute "$3" "${@:4}" 2>&1' \
		_ "$protocol" "$user" "$sql" "${host[@]}" "$@" <"$pwfile"
}

# valkey_admin <valkey-cli args> runs valkey-cli as zimam-admin, whose
# password is root's file.
valkey_admin() {
	# shellcheck disable=SC2016 # expanded by the inner bash
	in_c bash -c 'read -r REDISCLI_AUTH </etc/zimam/valkey/admin.pass; export REDISCLI_AUTH; exec valkey-cli --user zimam-admin -e "$@" 2>&1' _ "$@"
}

# valkey_as <user> <password file> <valkey-cli args> runs valkey-cli as a
# site's user. A reply that is an error comes back with a non-zero status.
valkey_as() {
	local user=$1 pwfile=$2
	shift 2
	# shellcheck disable=SC2016 # expanded by the inner bash
	in_c bash -c 'read -r REDISCLI_AUTH; export REDISCLI_AUTH; user=$1; shift; exec valkey-cli --user "$user" -e "$@" 2>&1' _ "$user" "$@" <"$pwfile"
}

# keys_in <site> <database> counts the keys that start with the site's
# prefix in one Valkey database.
keys_in() {
	valkey_admin -n "$2" --scan --pattern "s_$1:*" | wc -l
}

# keys_of <site> counts them in every Valkey database.
keys_of() {
	local total=0 db
	for db in $(seq 0 15); do
		total=$((total + $(keys_in "$1" "$db")))
	done
	echo "$total"
}
