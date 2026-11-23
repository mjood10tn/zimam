#!/usr/bin/env bash
# v0.5.0's sinks (Task 11): one more container, $E2E_CONTAINER-sinks, on
# lxdbr0, that plays the backup destinations and the mail server
# (server.sh): S3 through `rclone serve s3` over HTTPS, SFTP through OpenSSH
# and SMTP through smtp-sink.py. LXD only: on a VPS the backup scenario is
# skipped (README.md).
# up writes $E2E_WORK/sinks/sinks.env (0600): SINK_ADDRESS, S3_ENDPOINT,
# S3_ACCESS, S3_SECRET, S3_BUCKET. It also makes the server trust the sinks'
# CA and resolve the S3 name, as an owner's server trusts a real provider.
#   sinks.sh up                start it
#   sinks.sh authorize <key>   let one public key log in to SFTP as sftpbackup
#   sinks.sh mail              print the messages the SMTP sink took
#   sinks.sh s3-files          list the S3 sink's files with their sizes
#   sinks.sh s3-grep <text>    print the S3 sink's files that contain <text>
#   sinks.sh down              remove it
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

name=$E2E_CONTAINER-sinks
dir=$E2E_WORK/sinks
s3_name=s3-sink.$E2E_DOMAIN
bucket=zimam-e2e

sexec() {
	lxc exec "$name" -- "$@"
}

# The rclone release the agent pins, so the sink needs no second pin.
rclone_pin() {
	local src=$E2E_ROOT/agent/internal/rclone/binary.go arch
	arch=$(lxc exec "$name" -- dpkg --print-architecture)
	RCLONE_VERSION=$(grep -oP 'const pinVersion = "\K[0-9.]+' "$src")
	RCLONE_ZIP_SHA=$(awk -v a="\"$arch\": {" '$0 ~ a { getline; print }' "$src" | grep -oE '[0-9a-f]{64}')
	[[ -n $RCLONE_VERSION && ${#RCLONE_ZIP_SHA} == 64 ]] || die "can't read the rclone pin from $src"
}

up() {
	[[ $E2E_TARGET == lxd ]] || die "the sinks run in LXD only"
	mkdir -p "$dir"
	chmod 0700 "$dir"
	down_quietly
	log "the sinks: the container $name"
	lxc launch -q "$E2E_IMAGE" "$name" --profile default --profile "$E2E_PROFILE"
	local address=
	for _ in $(seq 60); do
		address=$(lxc list "$name" -c 4 -f csv | grep -oE '^[0-9.]+' || true)
		[[ -z $address ]] || break
		sleep 2
	done
	[[ -n $address ]] || die "the sinks got no IPv4 address"
	sexec cloud-init status --wait >/dev/null || true
	sexec bash -c 'cat >/root/sinks-server.sh' <"$E2E_ROOT/e2e/sinks/server.sh"
	sexec bash -c 'cat >/root/smtp-sink.py' <"$E2E_ROOT/e2e/sinks/smtp-sink.py"
	rclone_pin
	local access secret
	access=E2E$(python3 -I -c 'import secrets; print(secrets.token_hex(8).upper())')
	secret=$(python3 -I -c 'import secrets; print(secrets.token_urlsafe(30))')
	sexec bash /root/sinks-server.sh setup "$s3_name" "$access" "$secret" "$bucket" "$RCLONE_VERSION" "$RCLONE_ZIP_SHA"
	(
		umask 077
		printf 'SINK_ADDRESS=%s\nS3_ENDPOINT=%s\nS3_ACCESS=%s\nS3_SECRET=%s\nS3_BUCKET=%s\n' \
			"$address" "https://$s3_name" "$access" "$secret" "$bucket" >"$dir/sinks.env"
	)
	# The server trusts the sinks' CA and finds the S3 name, as it would a provider's.
	lxc file pull -q "$name/root/tls/ca.pem" "$dir/ca.pem"
	lxc file push -q --mode 0644 "$dir/ca.pem" "$E2E_CONTAINER/usr/local/share/ca-certificates/zimam-e2e-sinks.crt"
	in_c update-ca-certificates >/dev/null
	in_c bash -c "grep -v ' $s3_name\$' /etc/hosts >/etc/hosts.e2e-sinks; echo '$address $s3_name' >>/etc/hosts.e2e-sinks; cat /etc/hosts.e2e-sinks >/etc/hosts; rm /etc/hosts.e2e-sinks"
	# Any answer will do (rclone answers 403 without a signature); TLS must verify.
	in_c curl -sS -o /dev/null --max-time 10 "https://$s3_name/" || die "the server can't reach $s3_name over HTTPS"
	log "the sinks are up at $address: S3 https://$s3_name (bucket $bucket), SFTP sftpbackup@$address:22, SMTP $address:2525"
}

down_quietly() {
	lxc delete --force "$name" >/dev/null 2>&1 || true
}

case ${1:-} in
up) up ;;
authorize)
	[[ -n ${2:-} ]] || die "usage: sinks.sh authorize <key>"
	sexec bash /root/sinks-server.sh authorize "$2"
	;;
mail) sexec bash /root/sinks-server.sh mail ;;
s3-files) sexec find /srv/s3 -type f -printf '%s %P\n' ;;
s3-grep)
	[[ -n ${2:-} ]] || die "usage: sinks.sh s3-grep <text>"
	sexec grep -a -r -l -F -- "$2" /srv/s3 || true
	;;
down) down_quietly ;;
*) die "usage: sinks.sh up | authorize <key> | mail | s3-files | s3-grep <text> | down" ;;
esac
