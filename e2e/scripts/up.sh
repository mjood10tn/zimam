#!/usr/bin/env bash
# Starts the end-to-end environment (design §14.2): LXD with a fresh
# Ubuntu 26.04 container, and Pebble with pebble-challtestsrv on this host.
# Pebble's API gets a certificate from a CA made here on every run, so
# nothing pinned expires (Task 2's open question 3). Writes
# $E2E_WORK/state.env. Running it again starts over.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"

# Pebble v2.10.1's release tarballs, pinned by the Task 2 spike.
PEBBLE_URL=https://github.com/letsencrypt/pebble/releases/download/v2.10.1
PEBBLE_SHA256=4f2fcb5bca8c85c9cf73ad140fccfc0d2be40bd81ab99879c79b7b8a0b4f70ed
CHALLTESTSRV_SHA256=e93a5aa25ecdf3af2f9fbb2de32b0173e64a2eae81002a4ccfe35fa6f4f60b92

mkdir -p "$E2E_WORK"
rm -f "$E2E_WORK/state.env"

log "LXD"
if ! type -P lxc >/dev/null && [[ ! -x /snap/bin/lxc ]]; then
	as_root snap install lxd --channel=5.21/stable
fi
if ! lxc network show lxdbr0 >/dev/null 2>&1; then
	# The default "dir" pool would share the host's disk, and a GitHub
	# runner has about 10 GiB free: less than the installer's 15 GiB. A
	# sparse loop file only takes what the container writes.
	as_root "$(type -P lxd || echo /snap/bin/lxd)" init --auto --storage-backend=btrfs --storage-create-loop=30
fi
# Docker's FORWARD DROP policy would cut the container off (Task 2 spike).
if as_root iptables -L DOCKER-USER -n >/dev/null 2>&1; then
	as_root iptables -C DOCKER-USER -i lxdbr0 -j ACCEPT 2>/dev/null ||
		as_root iptables -I DOCKER-USER -i lxdbr0 -j ACCEPT
	as_root iptables -C DOCKER-USER -o lxdbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null ||
		as_root iptables -I DOCKER-USER -o lxdbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
fi
bridge=$(lxc network get lxdbr0 ipv4.address)
bridge=${bridge%/*}

log "the container $E2E_CONTAINER from $E2E_IMAGE"
lxc profile show zimam-e2e >/dev/null 2>&1 || lxc profile create zimam-e2e
lxc profile edit zimam-e2e <"$E2E_ROOT/e2e/lxd/profile.yaml"
lxc delete --force "$E2E_CONTAINER" >/dev/null 2>&1 || true
lxc launch -q "$E2E_IMAGE" "$E2E_CONTAINER" --profile default --profile zimam-e2e
cip=
for _ in $(seq 60); do
	cip=$(lxc list "$E2E_CONTAINER" -c 4 -f csv | grep -oE '^[0-9.]+' || true)
	[[ -z $cip ]] || break
	sleep 2
done
[[ -n $cip ]] || die "the container got no IPv4 address"
# cloud-init reports "degraded" for harmless warnings in containers.
in_c cloud-init status --wait >/dev/null || true
in_c systemctl is-active --quiet apt-daily.timer && die "the profile didn't stop the apt timers"

log "Pebble and pebble-challtestsrv"
p=$E2E_WORK/pebble
mkdir -p "$p"
for name in pebble pebble-challtestsrv; do
	if [[ ! -x $p/$name ]]; then
		sum=$PEBBLE_SHA256
		[[ $name == pebble ]] || sum=$CHALLTESTSRV_SHA256
		curl -fsSL --retry 3 -o "$p/$name.tar.gz" "$PEBBLE_URL/$name-linux-amd64.tar.gz"
		echo "$sum  $p/$name.tar.gz" | sha256sum -c --quiet
		tar -xzf "$p/$name.tar.gz" -C "$p"
		install -m 0755 "$p/$name-linux-amd64/linux/amd64/$name" "$p/$name"
	fi
done
# certbot's Python checks certificates strictly (VERIFY_X509_STRICT): the
# CA needs its key usage, and the server certificate its key identifiers.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 -subj "/CN=Zimam e2e Pebble API CA" \
	-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
	-keyout "$p/ca.key" -out "$p/ca.pem" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=pebble" -keyout "$p/api.key" -out "$p/api.csr" 2>/dev/null
openssl x509 -req -in "$p/api.csr" -CA "$p/ca.pem" -CAkey "$p/ca.key" -CAcreateserial -days 30 -out "$p/api.pem" \
	-extfile <(printf '%s\n' "subjectAltName=DNS:pebble,DNS:localhost,IP:127.0.0.1" "basicConstraints=critical,CA:FALSE" \
		"keyUsage=critical,digitalSignature" "extendedKeyUsage=serverAuth" \
		"subjectKeyIdentifier=hash" "authorityKeyIdentifier=keyid:always") 2>/dev/null
for unit in zimam-e2e-pebble zimam-e2e-challtestsrv; do
	as_root systemctl stop "$unit" 2>/dev/null || true
	as_root systemctl reset-failed "$unit" 2>/dev/null || true
done
# challtestsrv answers only Pebble's validation lookups: the names the
# harness adds (hosts.sh), and nothing by default.
as_root systemd-run -q --unit=zimam-e2e-challtestsrv --working-directory="$p" "$p/pebble-challtestsrv" \
	-dnsserver :8053 -management :8055 -defaultIPv4 "" -defaultIPv6 "" -http01 "" -https01 "" -tlsalpn01 "" -doh ""
as_root systemd-run -q --unit=zimam-e2e-pebble --working-directory="$p" -E PEBBLE_VA_NOSLEEP=1 -E PEBBLE_WFE_NONCEREJECT=0 \
	"$p/pebble" -config "$E2E_ROOT/e2e/pebble/pebble-config.json" -dnsserver 127.0.0.1:8053
for _ in $(seq 30); do
	curl -fsS --cacert "$p/ca.pem" -o /dev/null https://localhost:14000/dir 2>/dev/null &&
		curl -sS -o /dev/null http://localhost:8055/ 2>/dev/null && break
	sleep 1
done
curl -fsS --cacert "$p/ca.pem" -o /dev/null https://localhost:14000/dir || die "Pebble didn't start"

printf 'BRIDGE=%s\nCIP=%s\n' "$bridge" "$cip" >"$E2E_WORK/state.env"
log "up: bridge $bridge, container $cip"
