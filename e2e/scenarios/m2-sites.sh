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

# Nginx runs as www-data, which is in every site's group (§9.2), so it must
# not follow one site's link into another site's files: a compromised site
# could read the other's .env that way. A link to the site's own file works.
public=/home/s_static/site/current/public
secret=/home/s_php84/site/zimam-e2e-xsite.txt
in_c runuser -u s_php84 -- sh -c "umask 027 && echo 'php84 secret' >'$secret'"
in_c runuser -u s_static -- sh -c "ln -s '$secret' $public/xsite.txt && echo 'static own' >$public/own-target.txt && ln -s $public/own-target.txt $public/own.txt"
cleanup() {
	in_c rm -f "$public/xsite.txt" "$public/own.txt" "$public/own-target.txt" "$secret"
}
trap cleanup EXIT
# status <path> prints static's status code for the path, over HTTP.
status() {
	curl -sS -m 10 -o /dev/null -w '%{http_code}' --resolve "static.$E2E_DOMAIN:80:$CIP" "http://static.$E2E_DOMAIN/$1"
}
code=$(status xsite.txt) || die "FAIL isolation: static didn't answer for a link to php84's file"
[ "$code" != 200 ] || die "FAIL isolation: static served php84's file through a link"
own=$(status own.txt) || die "FAIL isolation: static didn't answer for a link to its own file"
[ "$own" = 200 ] || die "FAIL isolation: static answered $own for a link to its own file"
echo "PASS  isolation: static serves a link to its own file, and $code for one to php84's"
