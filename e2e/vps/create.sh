#!/usr/bin/env bash
# Creates the real server one run tests (e2e/vps/README.md): a throwaway SSH
# key, then a Hetzner Cloud server from the Ubuntu 26.04 image for
# E2E_GOARCH (amd64 or arm64), labelled zimam-vps=<E2E_VPS_TAG>. Waits for
# SSH and cloud-init, stops apt's own timers so nothing upgrades packages
# while the probes compare /etc, fetches Let's Encrypt's staging roots and
# checks them against the pins in STAGING_ROOT_SHA256, and waits for
# sslip.io to answer the test names. Writes everything the later steps need
# under $E2E_WORK/access/: server.env, state.env, staging-roots.pem,
# known_hosts, the key pair in ssh/, and vps.env with the settings for
# GITHUB_ENV. vps.yml hands that directory to the job that builds and tests,
# which has no token. Needs HCLOUD_TOKEN.
set -Eeuo pipefail

: "${E2E_WORK:?}" "${E2E_GOARCH:?amd64 or arm64}" "${E2E_VPS_TAG:?}" "${HCLOUD_TOKEN:?}" "${STAGING_ROOT_SHA256:?}"
here=$(cd "$(dirname "$0")" && pwd)
log() { printf '== %s\n' "$*" >&2; }
die() {
	printf 'e2e: %s\n' "$*" >&2
	exit 1
}

access=$E2E_WORK/access
mkdir -p "$access/ssh"
chmod 0700 "$access" "$access/ssh"
key=$access/ssh/id_ed25519
rm -f "$key" "$key.pub"
ssh-keygen -q -t ed25519 -N '' -C "zimam-vps-$E2E_VPS_TAG" -f "$key"

python3 -I "$here/hcloud.py" create "$E2E_VPS_TAG" "$E2E_GOARCH" "$key.pub" >"$access/server.env"
# shellcheck source=/dev/null
source "$access/server.env"
domain=${IPV4//./-}.sslip.io
cat >"$access/vps.env" <<EOF
E2E_TARGET=vps
E2E_SSH=root@$IPV4
E2E_SSH_KEY=$key
E2E_DOMAIN=$domain
EOF
set -a
# shellcheck source=/dev/null
source "$access/vps.env"
set +a
lxc() { E2E_WORK=$E2E_WORK "$here/lxc-ssh" "$@"; }
log "server $SERVER_ID ($SERVER_TYPE, $LOCATION, $IMAGE): $IPV4, $IPV6; names under $domain"

end=$((SECONDS + 300))
until lxc exec c -- true 2>/dev/null; do
	((SECONDS < end)) || die "no SSH to $IPV4 after 5 minutes"
	sleep 5
done
log "SSH answers"
# The host key this run accepted, so the next job checks it instead of
# accepting whatever answers.
cp "$E2E_WORK/known_hosts" "$access/known_hosts"
lxc exec c -- cloud-init status --wait >/dev/null || log "cloud-init ended with an error; carrying on"
lxc exec c -- systemctl stop apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service
lxc exec c -- bash -c 'while pgrep -x "apt|apt-get|dpkg|unattended-upgr" >/dev/null; do sleep 2; done'
# CLIENT, this host's address as the server sees it, is added by attach.sh
# in the job that tests: that job runs on another runner.
printf 'BRIDGE=127.0.0.1\nCIP=%s\nCIP6=%s\n' "$IPV4" "$IPV6" >"$access/state.env"
log "uname on the server: $(lxc exec c -- uname -m)"

roots=$access/staging-roots.pem
: >"$roots"
for name in letsencrypt-stg-root-x1 letsencrypt-stg-root-x2; do
	curl -fsS -o "$E2E_WORK/$name.pem" "https://letsencrypt.org/certs/staging/$name.pem"
	sum=$(sha256sum "$E2E_WORK/$name.pem" | cut -d' ' -f1)
	grep -qx "$sum" <<<"$STAGING_ROOT_SHA256" || die "$name.pem has SHA-256 $sum, not one of the pinned"
	cat "$E2E_WORK/$name.pem" >>"$roots"
done
log "Let's Encrypt's staging roots match their pins"

for name in panel static php84 php85; do
	end=$((SECONDS + 120))
	until [[ $(getent ahostsv4 "$name.$domain" | awk 'NR == 1 { print $1 }') == "$IPV4" ]]; do
		((SECONDS < end)) || die "$name.$domain doesn't resolve to $IPV4"
		sleep 5
	done
done
log "sslip.io answers the test names"
