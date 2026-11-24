#!/usr/bin/env bash
# v0.2.0's deploys (done-when 1 to 7), outside the browser: e2e/browser/
# deploy.spec.ts drives the panel and calls these steps for what only the
# server, the Git host (e2e/githost) or GitHub's side can see. The site is
# the Laravel site "laravel", deploying zimam-e2e/laravel-app from the Git
# host; each commit's release.txt holds a marker (v1, v2, ...) the app
# prints, with E2E_GREETING from its configuration.
#   v02-deploy.sh authorize         the site's deploy key, made as the site user, may fetch
#   v02-deploy.sh commit <marker>   a new commit on main with that marker
#   v02-deploy.sh mark              remember the newest agent job
#   v02-deploy.sh wait <command> <status> [trigger]
#                                   one <command> job since the mark ended <status>;
#                                   for site.deploy, its deployment has the trigger
#   v02-deploy.sh serves <marker> [greeting]
#                                   the site serves that commit (and greeting)
#   v02-deploy.sh env <greeting>    shared/.env holds E2E_GREETING=<greeting>, 0600
#   v02-deploy.sh loop start|stop <old> <new>
#                                   a request loop on the server; stop wants every
#                                   request 200, both markers, and <new> last
#   v02-deploy.sh webhook <marker>  a signed GitHub push of that commit, a replay of
#                                   it, and a push to another branch
#   v02-deploy.sh deploy-url        the deploy URL the browser kept deploys; the
#                                   one it replaced answers 404
#   v02-deploy.sh log               the app writes a line to its Laravel log; the
#                                   word is kept for the browser to find
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

site=laravel
user=s_$site
host=$site.$E2E_DOMAIN
dir=$E2E_WORK/v02
mkdir -p "$dir"
githost=$E2E_ROOT/e2e/githost/githost.sh

pass() { echo "PASS  $*"; }

# panel_post <url> <curl args...> POSTs to a panel URL from this host and
# prints the status code.
panel_post() {
	local url=$1 resolve=()
	shift
	[[ $E2E_TARGET == vps ]] || resolve=(--resolve "$E2E_PANEL:443:$CIP")
	curl -sS -m 30 -o "$dir/response" -w '%{http_code}' "${resolve[@]}" --cacert "$E2E_CA" -X POST "$@" "$url"
}

# stored_secret prints the site's webhook secret as the panel stores it,
# decrypted with the panel's own key.
stored_secret() {
	local php
	php=$(panel_php)
	# shellcheck disable=SC2016 # PHP's variables
	lxc exec "$E2E_CONTAINER" --cwd /home/zimam/panel/current --env HOME=/home/zimam -- runuser -u zimam -- "$php" -r '
		require "vendor/autoload.php";
		$app = require "bootstrap/app.php";
		$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
		echo App\Models\Site::where("name", $argv[1])->firstOrFail()->repository->webhook_secret, "\n";' -- "$site"
}

# get prints the site's page, requested on the server itself.
get() {
	in_c curl -fsS -m 10 --resolve "$host:80:127.0.0.1" "http://$host/${1:-}"
}

case ${1:-} in
authorize)
	key=$(panel_sql 'select r.deploy_key_public as k from site_repositories r join sites s on s.id = r.site_id where s.name = ?' "$site" |
		python3 -I -c 'import json, sys; print(json.load(sys.stdin)[0]["k"] or "")')
	[[ $key == ssh-ed25519\ * ]] || die "FAIL done-when 1: the panel has no deploy key for $site: $key"
	# Done-when 1: the key was made as the site user, in its home, and only
	# its public half reached the panel.
	owner=$(in_c stat -c '%U %a' "/home/$user/.ssh/zimam_deploy")
	[[ $owner == "$user 600" ]] || die "FAIL done-when 1: zimam_deploy is $owner, want $user 600"
	in_c runuser -u "$user" -- ssh-keygen -y -f "/home/$user/.ssh/zimam_deploy" | grep -qF "$key" ||
		die "FAIL done-when 1: the panel's key isn't the private key's public half"
	pass "done-when 1: the deploy key is $user's (0600), and the panel holds only its public half"
	bash "$githost" authorize "$key"
	;;
commit)
	marker=${2:?usage: commit <marker>}
	sha=$(bash "$githost" commit "$marker")
	[[ $sha =~ ^[0-9a-f]{40}$ ]] || die "the Git host's commit printed $sha"
	echo "$sha" >"$dir/commit-$marker"
	log "commit $sha holds $marker"
	;;
mark)
	jobs_mark >"$dir/mark"
	;;
wait)
	command=${2:?} want=${3:?}
	wait_jobs "$(cat "$dir/mark")" "$command" 1 "$want" || die "FAIL the $command job didn't end $want"
	if [[ $command == site.deploy && -n ${4:-} ]]; then
		row=$(panel_sql 'select d."trigger", d.status, d.release, d."commit" from deployments d join agent_jobs j on j.id = d.agent_job_id where j.id > ? order by d.id desc limit 1' "$(cat "$dir/mark")")
		python3 -I -c 'import json, sys; r = json.loads(sys.argv[1])[0]; sys.exit(0 if r["trigger"] == sys.argv[2] and r["status"] == sys.argv[3] else 1)' "$row" "$4" "$want" ||
			die "FAIL the deployment is $row, want trigger $4"
		pass "a $4 deployment ended $want: $row"
	else
		pass "the $command job ended $want"
	fi
	;;
serves)
	marker=${2:?} greeting=${3:-}
	body=$(get) || die "FAIL $host didn't answer"
	[[ $body == "Zimam e2e app release=$marker "* ]] || die "FAIL $host serves: $body, want release $marker"
	[[ -z $greeting || $body == *" greeting=$greeting" ]] || die "FAIL $host serves: $body, want greeting $greeting"
	pass "$host serves $marker${greeting:+ with greeting $greeting}"
	;;
env)
	greeting=${2:?}
	mode=$(in_c stat -c '%U %a' "/home/$user/site/shared/.env")
	[[ $mode == "$user 600" ]] || die "FAIL shared/.env is $mode, want $user 600"
	in_c runuser -u "$user" -- grep -qx "E2E_GREETING=$greeting" "/home/$user/site/shared/.env" ||
		die "FAIL shared/.env has no E2E_GREETING=$greeting"
	pass "the .env editor wrote shared/.env ($user, 0600) with E2E_GREETING=$greeting"
	;;
loop)
	remote=/var/tmp/zimam-e2e-deploy-loop
	case ${2:-} in
	start)
		# One line per request: the HTTP code (000 for no answer) and the
		# release marker, every 50 ms, on the server against its own Nginx.
		# shellcheck disable=SC2016 # expanded by the inner bash
		loop='
			while [[ ! -e $0/stop ]]; do
				code=$(curl -sS -m 10 -o "$0/body" -w "%{http_code}" --resolve "$1:80:127.0.0.1" "http://$1/" 2>>"$0/curl.err") || true
				printf "%s %s\n" "${code:-000}" "$(sed -n "s/.*release=\([^ ]*\).*/\1/p" "$0/body" 2>/dev/null)"
				rm -f "$0/body"
				sleep 0.05
			done'
		in_c bash -c "rm -rf $remote; mkdir -p $remote; setsid nohup bash -c $(printf '%q' "$loop") $remote $host \
			>$remote/results 2>$remote/loop.err </dev/null & echo \$! >$remote/pid"
		# The first request after a deploy can take seconds on a busy server.
		for _ in $(seq 1 20); do
			sleep 1
			in_c test -s "$remote/results" && break
		done
		in_c test -s "$remote/results" || die "the request loop isn't answering: $(in_c cat "$remote/loop.err" "$remote/curl.err" 2>/dev/null)"
		log "the request loop runs on the server"
		;;
	stop)
		old=${3:?} new=${4:?}
		in_c bash -c "touch $remote/stop; for _ in \$(seq 1 60); do kill -0 \$(cat $remote/pid) 2>/dev/null || break; sleep 0.5; done"
		in_c cat "$remote/results" >"$dir/loop-results"
		in_c cat "$remote/curl.err" >"$dir/loop-curl.err" 2>/dev/null || true
		in_c rm -rf "$remote"
		total=$(wc -l <"$dir/loop-results")
		bad=$(awk '$1 != 200' "$dir/loop-results" | wc -l)
		if ((bad > 0)); then
			awk '$1 != 200' "$dir/loop-results" | sort | uniq -c >&2
			cat "$dir/loop-curl.err" >&2
			die "FAIL done-when 2: $bad of $total requests during the deploy didn't answer 200"
		fi
		((total >= 50)) || die "FAIL done-when 2: only $total requests ran"
		grep -qx "200 $old" "$dir/loop-results" || die "FAIL done-when 2: the loop never saw $old"
		grep -qx "200 $new" "$dir/loop-results" || die "FAIL done-when 2: the loop never saw $new"
		[[ $(tail -n 1 "$dir/loop-results") == "200 $new" ]] || die "FAIL done-when 2: the last answer was $(tail -n 1 "$dir/loop-results")"
		# Once the new release answered, the old one never did again.
		after=$(awk -v n="$new" -v o="$old" '$2 == n { seen = 1 } seen && $2 == o { c++ } END { print c + 0 }' "$dir/loop-results")
		((after == 0)) || die "FAIL done-when 2: $old answered $after times after $new did"
		pass "done-when 2: $total requests across the deploy from $old to $new, all 200 ($(grep -cx "200 $new" "$dir/loop-results") on $new)"
		;;
	*) die "usage: loop start|stop <old> <new>" ;;
	esac
	;;
webhook)
	marker=${2:?}
	sha=$(cat "$dir/commit-$marker")
	secret=$(stored_secret)
	[[ $secret =~ ^[0-9a-f]{64}$ ]] || die "the panel's stored webhook secret isn't 64 hex characters"
	# The browser kept the secret the panel showed after the last Regenerate.
	[[ $secret == "$(cat "$E2E_WORK/v02-webhook-secret")" ]] || die "FAIL the stored webhook secret isn't the one the panel showed last"
	url="https://$E2E_PANEL/hooks/github/$site"
	# deliver <file> <delivery> <event> signs the body with the secret, as
	# GitHub does (X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of the raw
	# body>), POSTs it, and prints the status code.
	deliver() {
		local sig
		sig=$(python3 -I -c 'import hashlib, hmac, sys; print("sha256=" + hmac.new(sys.argv[1].encode(), open(sys.argv[2], "rb").read(), hashlib.sha256).hexdigest())' "$secret" "$1")
		panel_post "$url" -H 'Content-Type: application/json' -H "X-GitHub-Event: $3" -H "X-GitHub-Delivery: $2" \
			-H "X-Hub-Signature-256: $sig" -H 'User-Agent: GitHub-Hookshot/e2e' --data-binary "@$1"
	}
	# push <ref> <after> writes a push event's body, as GitHub sends it.
	push() {
		python3 -I -c '
import json, sys
ref, after = sys.argv[1], sys.argv[2]
print(json.dumps({"ref": ref, "before": "0" * 40, "after": after, "created": False, "deleted": False, "forced": False,
                  "repository": {"full_name": "zimam-e2e/laravel-app", "default_branch": "main"},
                  "pusher": {"name": "e2e"}, "commits": [{"id": after, "message": "e2e"}], "head_commit": {"id": after}}))' "$1" "$2"
	}
	push refs/heads/main "$sha" >"$dir/push.json"
	delivery=$(python3 -I -c 'import uuid; print(uuid.uuid4())')
	code=$(deliver "$dir/push.json" "$delivery" push)
	[[ $code == 202 ]] || die "FAIL done-when 4: the signed push answered $code: $(cat "$dir/response")"
	pass "done-when 4: a signed push of $marker ($sha) answered 202: $(cat "$dir/response")"
	code=$(deliver "$dir/push.json" "$delivery" push)
	[[ $code == 202 && $(cat "$dir/response") == *already* ]] || die "FAIL done-when 4: the replayed delivery answered $code: $(cat "$dir/response")"
	pass "done-when 4: the same delivery again is a duplicate: $(cat "$dir/response")"
	push refs/heads/other "$sha" >"$dir/push-other.json"
	code=$(deliver "$dir/push-other.json" "$(python3 -I -c 'import uuid; print(uuid.uuid4())')" push)
	[[ $code == 202 && $(cat "$dir/response") == *branch* ]] || die "FAIL done-when 4: a push to another branch answered $code: $(cat "$dir/response")"
	pass "done-when 4: a push to another branch is ignored: $(cat "$dir/response")"
	;;
deploy-url)
	url=$(cat "$E2E_WORK/v02-deploy-url")
	old=$(cat "$E2E_WORK/v02-deploy-url-replaced")
	[[ $url == "https://$E2E_PANEL/hooks/deploy/$site/"* && $old == "https://$E2E_PANEL/hooks/deploy/$site/"* && $url != "$old" ]] ||
		die "the browser kept no deploy URLs"
	code=$(panel_post "$old")
	[[ $code == 404 ]] || die "FAIL done-when 5: the replaced deploy URL answered $code"
	pass "done-when 5: the replaced deploy URL answers 404"
	code=$(panel_post "$url")
	[[ $code == 202 ]] || die "FAIL done-when 5: the deploy URL answered $code: $(cat "$dir/response")"
	pass "done-when 5: the deploy URL answered 202: $(cat "$dir/response")"
	;;
log)
	word=e2e-$(date +%s)-$RANDOM
	get "?log=$word" >/dev/null || die "FAIL $host didn't answer ?log="
	in_c runuser -u "$user" -- grep -q "zimam-e2e $word" "/home/$user/site/shared/storage/logs/laravel.log" ||
		die "FAIL the app's line isn't in shared/storage/logs/laravel.log"
	echo "$word" >"$E2E_WORK/v02-log-word"
	pass "done-when 7: the app wrote 'zimam-e2e $word' to its Laravel log"
	;;
*) die "usage: v02-deploy.sh authorize | commit | mark | wait | serves | env | loop | webhook | deploy-url | log" ;;
esac
