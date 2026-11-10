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

# Nginx runs as www-data, which is in every site's group and reads every
# htpasswd (§9.2), so it must not follow a link into another owner's files:
# not one below a site's root, and not one in the root's own path, which the
# site owns (current, public) and can repoint. A link to the site's own file,
# and the site's own current -> releases/initial, still work. m1-install.sh
# checks the same for the panel's root.
site=/home/s_static/site
public=$site/current/public
secret=/home/s_php84/site/current/public/zimam-e2e-xsite.txt
in_c runuser -u s_php84 -- sh -c "umask 027 && echo 'php84 secret' >'$secret'"
in_c runuser -u s_static -- sh -c "ln -s '$secret' $public/xsite.txt && echo 'static own' >$public/own-target.txt && ln -s $public/own-target.txt $public/own.txt"
cleanup() {
	in_c runuser -u s_static -- sh -c "cd $site && ln -sfn releases/initial cur.tmp && mv -T cur.tmp current && rm -rf x
		cd releases/initial && if [ -L public ]; then rm public && mv public.orig public; fi" || true
	in_c rm -f "$public/xsite.txt" "$public/own.txt" "$public/own-target.txt" "$secret"
}
trap cleanup EXIT
# status <host> <path> prints the status code for the path, over HTTP.
status() {
	curl -sS -m 10 -o /dev/null -w '%{http_code}' --resolve "$1:80:$CIP" "http://$1/$2"
}
# refused <what> <host> <path> wants an answer other than 200.
refused() {
	local code
	code=$(status "$2" "$3") || die "FAIL isolation: $2 didn't answer for $1"
	[ "$code" != 200 ] || die "FAIL isolation: $2 served $1 (200)"
	echo "PASS  isolation: $2 answers $code for $1"
}
static=static.$E2E_DOMAIN
own=$(status "$static" own.txt) || die "FAIL isolation: static didn't answer for a link to its own file"
[ "$own" = 200 ] || die "FAIL isolation: static answered $own for a link to its own file"
echo "PASS  isolation: static serves a link to its own file"
refused "a link to php84's file below its root" "$static" xsite.txt

# The site repoints current at a directory whose public is php84's Nginx folder.
in_c runuser -u s_static -- sh -c "cd $site && mkdir x && ln -s /etc/nginx/zimam/php84 x/public && ln -s x cur.tmp && mv -T cur.tmp current"
refused "php84's security.conf through a repointed current" "$static" security.conf
in_c runuser -u s_static -- sh -c "cd $site && ln -sfn releases/initial cur.tmp && mv -T cur.tmp current && rm -rf x"

# The site replaces its release's public with a link to php84's.
in_c runuser -u s_static -- sh -c "cd $site/releases/initial && mv public public.orig && ln -s /home/s_php84/site/current/public public"
refused "php84's file through a repointed public" "$static" zimam-e2e-xsite.txt
in_c runuser -u s_static -- sh -c "cd $site/releases/initial && rm public && mv public.orig public"
code=$(status "$static" "") || die "FAIL isolation: static didn't answer once restored"
[ "$code" = 200 ] || die "FAIL isolation: static answered $code once restored"
echo "PASS  isolation: static answers 200 once restored"
