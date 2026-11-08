#!/usr/bin/env bash
# Done-when 4 (design §2, §9.2), outside the browser: over HTTP, the static
# site shows its placeholder, and each PHP site's placeholder prints its
# own version.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

# get <name> prints the site's body over HTTP, from this host.
get() {
	curl -fsS -m 10 --resolve "$1.$E2E_DOMAIN:80:$CIP" "http://$1.$E2E_DOMAIN/"
}

body=$(get static) || die "FAIL done-when 4: static didn't answer"
[[ $body == *"Zimam site static is ready."* ]] || die "FAIL done-when 4: static answered: $body"
echo "PASS  done-when 4: static serves its placeholder"
for v in 8.4 8.5; do
	name=php${v/./}
	body=$(get "$name") || die "FAIL done-when 4: $name didn't answer"
	[[ $body == "Zimam site $name is ready. PHP $v."* ]] || die "FAIL done-when 4: $name answered: $body"
	echo "PASS  done-when 4: $name prints PHP ${body##* PHP }"
done
