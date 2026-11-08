#!/usr/bin/env bash
# The real-server run's IPv4 and IPv6 checks (design §14.3). From this host,
# over IPv4 and real DNS (sslip.io, no --resolve): the panel and each site
# answer over HTTPS with Let's Encrypt staging certificates. GitHub's
# runners have no IPv6, so the IPv6 side runs on the server: Nginx listens
# on [::]:80 and [::]:443, UFW allows 22, 80 and 443 over v6, and the panel
# and each site answer `curl -6` at the server's own address. A v6 rule
# that drops outside traffic would still pass (README.md).
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state
[[ $E2E_TARGET == vps ]] || die "ipv6.sh runs against a VPS only"

failed=0
check() {
	if [[ $2 == "$3" ]]; then
		echo "PASS  $1: $2"
	else
		echo "FAIL  $1: $2, want $3"
		failed=1
	fi
}

lxc file push -q "$E2E_CA" "$E2E_CONTAINER/var/tmp/zimam-e2e-roots.pem"
for name in "${E2E_NAMES[@]}"; do
	want=200
	[[ $name == panel.* ]] && want=302
	got=$(curl -4 -sS -m 15 -o /dev/null -w '%{http_code}' --cacert "$E2E_CA" "https://$name/" || echo curl-failed)
	check "$name over IPv4 and real DNS" "$got" "$want"
	got=$(in_c curl -6 -sS -m 15 -o /dev/null -w '%{http_code}' --cacert /var/tmp/zimam-e2e-roots.pem \
		--resolve "$name:443:[$CIP6]" "https://$name/" || echo curl-failed)
	check "$name over IPv6 at $CIP6" "$got" "$want"
done
in_c rm -f /var/tmp/zimam-e2e-roots.pem

listening=$(in_c ss -Hltn)
for port in 80 443; do
	if grep -q "\[::\]:$port " <<<"$listening"; then
		echo "PASS  Nginx listens on [::]:$port"
	else
		echo "FAIL  nothing listens on [::]:$port"
		failed=1
	fi
done
ufw=$(in_c ufw status)
check "UFW" "$(head -n 1 <<<"$ufw")" "Status: active"
for port in 22 80 443; do
	if grep -Eq "^$port/tcp \(v6\) +ALLOW" <<<"$ufw"; then
		echo "PASS  UFW allows $port/tcp over IPv6"
	else
		echo "FAIL  UFW has no IPv6 rule for $port/tcp:"
		echo "$ufw"
		failed=1
	fi
done
exit "$failed"
