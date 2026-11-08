#!/usr/bin/env bash
# Creates the real server one run tests (e2e/vps/README.md): a throwaway SSH
# key, then a Hetzner Cloud server from the Ubuntu 26.04 image for
# E2E_GOARCH (amd64 or arm64), labelled zimam-vps=<E2E_VPS_TAG>. Waits for
# SSH and cloud-init, stops apt's own timers so nothing upgrades packages
# while the probes compare /etc, fetches Let's Encrypt's staging roots and
# checks them against the pins in STAGING_ROOT_SHA256, and waits for
# sslip.io to answer the test names. Writes state.env, and vps.env with the
# settings the later steps need (vps.yml adds it to GITHUB_ENV).
# Needs HCLOUD_TOKEN.
set -Eeuo pipefail

: "${E2E_WORK:?}" "${E2E_GOARCH:?amd64 or arm64}" "${E2E_VPS_TAG:?}" "${HCLOUD_TOKEN:?}" "${STAGING_ROOT_SHA256:?}"
here=$(cd "$(dirname "$0")" && pwd)
log() { printf '== %s\n' "$*" >&2; }
die() {
	printf 'e2e: %s\n' "$*" >&2
	exit 1
}

mkdir -p "$E2E_WORK/ssh"
chmod 0700 "$E2E_WORK/ssh"
key=$E2E_WORK/ssh/id_ed25519
rm -f "$key" "$key.pub"
ssh-keygen -q -t ed25519 -N '' -C "zimam-vps-$E2E_VPS_TAG" -f "$key"

python3 -I "$here/hcloud.py" create "$E2E_VPS_TAG" "$E2E_GOARCH" "$key.pub" >"$E2E_WORK/server.env"
# shellcheck source=/dev/null
source "$E2E_WORK/server.env"
domain=${IPV4//./-}.sslip.io
cat >"$E2E_WORK/vps.env" <<EOF
E2E_TARGET=vps
E2E_SSH=root@$IPV4
E2E_SSH_KEY=$key
E2E_DOMAIN=$domain
EOF
set -a
# shellcheck source=/dev/null
source "$E2E_WORK/vps.env"
set +a
lxc() { E2E_WORK=$E2E_WORK "$here/lxc-ssh" "$@"; }
log "server $SERVER_ID ($SERVER_TYPE, $LOCATION, $IMAGE): $IPV4, $IPV6; names under $domain"

end=$((SECONDS + 300))
until lxc exec c -- true 2>/dev/null; do
	((SECONDS < end)) || die "no SSH to $IPV4 after 5 minutes"
	sleep 5
done
log "SSH answers"
lxc exec c -- cloud-init status --wait >/dev/null || log "cloud-init ended with an error; carrying on"
lxc exec c -- systemctl stop apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service
lxc exec c -- bash -c 'while pgrep -x "apt|apt-get|dpkg|unattended-upgr" >/dev/null; do sleep 2; done'
# shellcheck disable=SC2016 # expanded on the server
client=$(lxc exec c -- bash -c 'echo ${SSH_CLIENT%% *}')
[[ -n $client ]] || die "the server didn't report this host's address"
printf 'BRIDGE=127.0.0.1\nCIP=%s\nCIP6=%s\nCLIENT=%s\n' "$IPV4" "$IPV6" "$client" >"$E2E_WORK/state.env"
log "this host is $client to the server; uname: $(lxc exec c -- uname -m)"

roots=$E2E_WORK/staging-roots.pem
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
