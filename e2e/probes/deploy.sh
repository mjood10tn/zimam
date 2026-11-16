#!/usr/bin/env bash
# The deploy probes (v0.2.0, R02-D1 to R02-D11), as zimam through the agent,
# on the throwaway Laravel site probe-deploy, which deploys the Git host's
# zimam-e2e/laravel-app (e2e/githost) and is destroyed at the end; run-all.sh
# runs them between its two file snapshots. Nothing a site's deploy script
# or its user plants may reach outside the home:
#   - a deploy script that writes /etc/zimam-probe, or reads /root or
#     /etc/shadow, fails as the site user, and the live release stays;
#   - a planted shared/.env -> /etc/shadow makes site.env.read fail without
#     shadow's content, and site.env.write never writes through it;
#   - planted releases/<id> -> / and -> a folder of the user's outside the
#     home are removed by the next deploy's pruning, never followed;
#   - a commit that isn't on the branch and an unknown release are refused,
#     and site.releases of an unknown site answers not_found (the malformed
#     values of every field are validation.sh's);
#   - a GitHub push with a bad signature, and a signed one over the panel's
#     1 MiB body limit, start no job.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
load_state

site=probe-deploy
user=s_$site
home=/home/$user
githost=$E2E_ROOT/e2e/githost/githost.sh
canary=/var/tmp/zimam-e2e-canary

# job_data <job> prints job.status's data as JSON.
job_data() {
	request job.status "{\"job\":\"$1\"}" | python3 -I -c 'import json, sys; print(json.dumps(json.load(sys.stdin)["data"]))'
}

# job_log <job> prints the job's log lines.
job_log() {
	request job.follow "{\"job\":\"$1\",\"from_offset\":0}" | python3 -I -c '
import json, sys
for line in sys.stdin:
    r = json.loads(line)
    if r.get("type") == "log":
        print(r.get("line", ""))'
}

# deploy <params> submits a site.deploy of probe-deploy without Composer,
# waits for it, and prints "<job> <status> <message>".
deploy() {
	local job
	job=$(submit site.deploy "{\"name\":\"$site\",\"composer\":false,\"trigger\":\"manual\"$1}") || return 1
	echo "$job $(job_end "$job")"
}

live() {
	in_c readlink "$home/site/current"
}

run_job site.provision "{\"name\":\"$site\",\"domain\":\"$site.$E2E_DOMAIN\",\"type\":\"laravel\",\"php_version\":\"8.4\"}"
job=$(submit site.repo "{\"name\":\"$site\",\"repository\":\"zimam-e2e/laravel-app\",\"branch\":\"main\"}") || die "site.repo wasn't accepted"
status=$(job_end "$job")
[[ $status == succeeded* ]] || die "site.repo ended $status"
key=$(job_data "$job" | python3 -I -c 'import json, sys; print(json.load(sys.stdin)["result"]["public_key"])')
bash "$githost" authorize "$key"
read -r job status message < <(deploy '')
[[ $status == succeeded ]] || die "the first deploy of $site ended $status $message"
first=$(live)
releases=$(in_c ls "$home/site/releases")
pass "$site deployed $first"

# Deploy scripts run as the site user: root's files are out of reach, and
# the failed build leaves the live release alone.
# shellcheck disable=SC2016 # the scripts are the site user's
for script in 'echo probe >/etc/zimam-probe' 'ls /root' 'cat /etc/shadow'; do
	read -r job status message < <(deploy ",\"script\":$(python3 -I -c 'import json, sys; print(json.dumps(sys.argv[1]))' "$script")")
	if [[ $status != failed ]]; then
		fail "a deploy running '$script' ended $status $message"
	elif ! job_log "$job" | grep -q 'Permission denied'; then
		fail "a deploy running '$script' failed, but not for a denied permission: $message"
	else
		pass "a deploy running '$script' failed as $user: Permission denied"
	fi
done
if in_c test -e /etc/zimam-probe; then
	fail "/etc/zimam-probe exists"
	in_c rm -f /etc/zimam-probe
else
	pass "/etc/zimam-probe was never written"
fi
if [[ $(live) == "$first" ]]; then pass "current still points at $first"; else fail "current moved to $(live)"; fi
leftover=$(in_c ls "$home/site/releases")
if [[ $leftover == "$releases" ]]; then pass "the failed builds left no release behind"; else fail "releases/ holds: $leftover, want: $releases"; fi

# A well-formed commit that isn't on the branch.
read -r job status message < <(deploy ",\"commit\":\"$(printf '%040d' 0)\"")
if [[ $status == failed && $message == *"isn't on the branch"* ]]; then
	pass "a deploy of a commit that isn't on main fails: $message"
else
	fail "a deploy of a commit that isn't on main ended $status $message"
fi
probe "site.rollback to a release that isn't kept" not_found \
	"{$env_ok,\"command\":\"site.rollback\",\"params\":{\"name\":\"$site\",\"release\":\"20000101000000-0000000\"}}"
# A routine read that answers not_found is a result, not a rejected request:
# no audit entry (as archive-names.sh's unlisted archive).
before=$(audit_lines)
reply=$(request site.releases '{"name":"nosuchsite"}')
if [[ $(field code <<<"$reply") == not_found && $(audit_lines) == "$before" ]]; then
	pass "site.releases of a site that doesn't exist answers not_found, and adds no audit entry"
else
	fail "site.releases of a site that doesn't exist answers ${reply:0:200}; audit entries: $(audit_since "$before")"
fi

# shared/.env -> /etc/shadow, planted by the site user.
shadow=$(in_c sha256sum /etc/shadow)
in_c runuser -u "$user" -- mv "$home/site/shared/.env" "$home/site/shared/.env.e2e-saved"
in_c runuser -u "$user" -- ln -s /etc/shadow "$home/site/shared/.env"
reply=$(request site.env.read "{\"name\":\"$site\"}")
if [[ $reply == *root:* ]]; then
	fail "site.env.read through the planted link shows shadow's content"
elif [[ $(field type <<<"$reply") == error ]]; then
	pass "site.env.read through shared/.env -> /etc/shadow answers $(field code <<<"$reply"), and nothing of shadow"
else
	fail "site.env.read through the planted link answers ${reply:0:300}"
fi
job=$(submit site.env.write "{\"name\":\"$site\",\"content\":\"E2E_PROBE=1\\n\"}") || die "site.env.write wasn't accepted"
status=$(job_end "$job")
# The write never goes through the link: it fails, or its rename replaces
# the link with the site user's own 0600 file. Shadow is unchanged either way.
if [[ $(in_c sha256sum /etc/shadow) != "$shadow" ]]; then
	fail "site.env.write through the planted link changed /etc/shadow"
elif [[ $status == failed* ]]; then
	pass "site.env.write through the planted link failed, and /etc/shadow is unchanged"
elif [[ $status == succeeded* && $(in_c stat -c '%F %U %a' "$home/site/shared/.env") == "regular file $user 600" ]]; then
	pass "site.env.write replaced the planted link with $user's own 0600 .env, and /etc/shadow is unchanged"
else
	fail "site.env.write through the planted link ended $status, .env is $(in_c stat -c '%F %U %a' "$home/site/shared/.env")"
fi
in_c runuser -u "$user" -- rm "$home/site/shared/.env"
in_c runuser -u "$user" -- mv "$home/site/shared/.env.e2e-saved" "$home/site/shared/.env"

# releases/<id> -> / and -> a folder the user owns outside its home, then a
# deploy whose prune removes every release the state doesn't list.
in_c install -d -o "$user" -g "$user" -m 0700 "$canary"
in_c runuser -u "$user" -- sh -c "echo canary >$canary/file"
in_c runuser -u "$user" -- ln -s / "$home/site/releases/20000101000000-0000000"
in_c runuser -u "$user" -- ln -s "$canary" "$home/site/releases/20000101000001-0000000"
sleep 1 # release ids are per second
read -r job status message < <(deploy '')
[[ $status == succeeded ]] || fail "the deploy after the planted links ended $status $message"
left=$(in_c find "$home/site/releases" -maxdepth 1 -type l)
if [[ -z $left ]]; then
	pass "the deploy's prune removed both planted links"
else
	fail "the planted links are still there: $left"
fi
if [[ $(in_c cat "$canary/file") == canary ]] && in_c test -d /etc && in_c test -f /usr/bin/env; then
	pass "the prune didn't follow them: $canary/file and / are intact"
else
	fail "the prune followed a planted link"
fi
in_c rm -rf "$canary"

# GitHub pushes the panel must refuse before any job: a bad signature (404,
# as for an unknown site), and a correct one on a body over 1 MiB, which
# Nginx refuses (413) before the panel reads it.
if [[ -s $E2E_WORK/v02-webhook-secret ]]; then
	resolve=()
	[[ $E2E_TARGET == vps ]] || resolve=(--resolve "$E2E_PANEL:443:$CIP")
	hook="https://$E2E_PANEL/hooks/github/laravel"
	body=$E2E_WORK/probe-push.json
	sign() {
		python3 -I -c 'import hashlib, hmac, sys; print("sha256=" + hmac.new(sys.argv[1].encode(), open(sys.argv[2], "rb").read(), hashlib.sha256).hexdigest())' "$1" "$2"
	}
	post() {
		curl -sS -m 30 -o /dev/null -w '%{http_code}' "${resolve[@]}" --cacert "$E2E_CA" -X POST -H 'Content-Type: application/json' \
			-H 'X-GitHub-Event: push' -H "X-GitHub-Delivery: $(python3 -I -c 'import uuid; print(uuid.uuid4())')" \
			-H "X-Hub-Signature-256: $1" --data-binary "@$body" "$hook"
	}
	head=$(printf "%040d" 1)
	before=$(jobs_mark)
	printf '{"ref":"refs/heads/main","after":"%s","deleted":false}' "$head" >"$body"
	code=$(post "sha256=$(printf '%064d' 0)")
	if [[ $code == 404 ]]; then pass "a push with a bad signature answers 404"; else fail "a push with a bad signature answers $code"; fi
	python3 -I -c 'import json, sys; print(json.dumps({"ref": "refs/heads/main", "after": sys.argv[1], "deleted": False, "pad": "x" * (1 << 20)}))' "$head" >"$body"
	code=$(post "$(sign "$(cat "$E2E_WORK/v02-webhook-secret")" "$body")")
	if [[ $code == 413 ]]; then pass "a signed push over 1 MiB ($(wc -c <"$body") bytes) answers 413"; else fail "a signed push over 1 MiB answers $code"; fi
	rm -f "$body"
	if [[ $(jobs_mark) == "$before" ]]; then pass "neither push started a job"; else fail "a refused push started a job"; fi
else
	fail "no webhook secret from the v02-deploy browser run, so the webhook probes didn't run"
fi

run_job site.destroy "{\"name\":\"$site\"}"
if in_c test -e "$home"; then fail "$home is still there"; else pass "$site is destroyed"; fi
exit "$failed"
