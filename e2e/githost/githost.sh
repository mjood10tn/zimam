#!/usr/bin/env bash
# The run's Git host, which plays GitHub for deploys (Task 12). It runs
# server.sh: OpenSSH with a user git whose shell is git-shell, holding
# zimam-e2e/laravel-app.git seeded from e2e/fixtures/laravel-app. Its host
# key is made when it starts and never leaves the run.
#   - LXD: a second container, $E2E_CONTAINER-git, on lxdbr0; the server
#     reaches it at its address, port 22.
#   - VPS (no LXD on the runner): a Docker container on the runner, its
#     port 22 on the runner's 127.0.0.1:2222, and a reverse SSH tunnel that
#     makes the server's own 127.0.0.1:2222 reach it.
# up also sets e2e_git_host in the server's agent.json, which only e2e
# builds accept: site.repo then pins the Git host's key under the name
# github.com, and git's ssh connects to it (agent/internal/deploy/
# githost_e2e.go). Everything else a deploy does is the release build's.
#   githost.sh up               start it, seed the repository, point the server at it
#   githost.sh authorize <key>  let a site's deploy key fetch
#   githost.sh commit <marker>  commit release.txt = <marker> on main; prints the commit
#   githost.sh down             remove it, and the tunnel
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

name=$E2E_CONTAINER-git
dir=$E2E_WORK/githost
# The port the server reaches the Git host on, over the tunnel, on a VPS.
tunnel_port=2222

# gexec runs a command in the Git host as root, stdin passed through.
gexec() {
	if [[ $E2E_TARGET == vps ]]; then
		docker exec -i "$name" "$@"
	else
		lxc exec "$name" -- "$@"
	fi
}

server() {
	gexec bash /root/githost-server.sh "$@"
}

# tunnel keeps the reverse forward up, reconnecting until down.sh stops it.
tunnel() {
	local ssh=(ssh -N -i "$E2E_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes
		-o "UserKnownHostsFile=$E2E_WORK/known_hosts" -o ExitOnForwardFailure=yes -o ServerAliveInterval=15
		-o ServerAliveCountMax=4 -o LogLevel=ERROR -R "127.0.0.1:$tunnel_port:127.0.0.1:$tunnel_port" "$E2E_SSH")
	rm -f "$dir/tunnel.stop"
	# shellcheck disable=SC2016 # expanded by the inner bash
	setsid nohup bash -c 'while [[ ! -e $0 ]]; do "${@}"; sleep 2; done' "$dir/tunnel.stop" "${ssh[@]}" \
		>"$dir/tunnel.log" 2>&1 </dev/null &
	echo $! >"$dir/tunnel.pid"
}

up() {
	mkdir -p "$dir"
	down_quietly
	local address port
	if [[ $E2E_TARGET == vps ]]; then
		log "the Git host: a Docker container on this runner, reached over a tunnel from the server"
		docker run -d -q --name "$name" -p "127.0.0.1:$tunnel_port:22" "$E2E_IMAGE" sleep infinity >/dev/null
		address=127.0.0.1 port=$tunnel_port
	else
		log "the Git host: the container $name"
		lxc launch -q "$E2E_IMAGE" "$name" --profile default --profile zimam-e2e
		address=
		for _ in $(seq 60); do
			address=$(lxc list "$name" -c 4 -f csv | grep -oE '^[0-9.]+' || true)
			[[ -z $address ]] || break
			sleep 2
		done
		[[ -n $address ]] || die "the Git host got no IPv4 address"
		gexec cloud-init status --wait >/dev/null || true
		port=22
	fi
	gexec bash -c 'cat >/root/githost-server.sh' <"$E2E_ROOT/e2e/githost/server.sh"
	gexec bash -c 'rm -rf /root/fixture && mkdir /root/fixture'
	tar -C "$E2E_ROOT/e2e/fixtures/laravel-app" -c . | gexec tar -x -C /root/fixture
	local key
	key=$(server setup | tail -n 1)
	[[ $key =~ ^ssh-ed25519\ [A-Za-z0-9+/]+=*$ ]] || die "the Git host printed no host key: $key"
	printf 'GIT_ADDRESS=%s\nGIT_PORT=%s\nGIT_HOST_KEY=%q\n' "$address" "$port" "$key" >"$dir/githost.env"
	if [[ $E2E_TARGET == vps ]]; then
		tunnel
	fi
	# The server reaches the Git host's sshd: its banner comes back.
	local banner=
	for _ in $(seq 30); do
		banner=$(in_c timeout 5 bash -c "exec 3<>/dev/tcp/$address/$port && head -c 7 <&3" 2>/dev/null || true)
		[[ $banner != SSH-2.0 ]] || break
		sleep 2
	done
	[[ $banner == SSH-2.0 ]] || die "the server can't reach the Git host at $address:$port"
	# Spike amendment A11: what the server has before site.repo installs
	# what's missing (on a VPS, Hetzner's image).
	# shellcheck disable=SC2016 # dpkg-query's format
	log "git and unzip on the server before site.repo: $(in_c bash -c 'for p in git unzip; do dpkg-query -W -f "\${Package} \${db:Status-Status} \${Version}; " "$p" 2>/dev/null || echo -n "$p not installed; "; done')"
	# agent.json: written as bootstrap writes it, root's, 0600, renamed into place.
	# shellcheck disable=SC2016 # Python's code
	in_c python3 -I -c '
import json, os, sys
path = "/etc/zimam/agent.json"
with open(path) as f:
    config = json.load(f)
config["e2e_git_host"] = {"address": sys.argv[1], "port": int(sys.argv[2]), "host_keys": [sys.argv[3]]}
tmp = path + ".e2e-git"
fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    f.write(json.dumps(config, indent=2) + "\n")
os.replace(tmp, path)' "$address" "$port" "$key"
	log "the Git host is up at $address:$port, its key $key; agent.json points deploys at it"
}

down_quietly() {
	if [[ -f $dir/tunnel.pid ]]; then
		touch "$dir/tunnel.stop"
		kill -- "-$(cat "$dir/tunnel.pid")" 2>/dev/null || true
		rm -f "$dir/tunnel.pid"
	fi
	if [[ $E2E_TARGET == vps ]]; then
		docker rm -f "$name" >/dev/null 2>&1 || true
	else
		lxc delete --force "$name" >/dev/null 2>&1 || true
	fi
}

case ${1:-} in
up) up ;;
authorize)
	[[ -n ${2:-} ]] || die "usage: githost.sh authorize <ssh-ed25519 key>"
	server authorize "$2"
	log "the Git host lets ${2:0:40}… fetch"
	;;
commit)
	[[ -n ${2:-} ]] || die "usage: githost.sh commit <marker>"
	server commit "$2" | tail -n 1
	;;
down) down_quietly ;;
*) die "usage: githost.sh up | authorize <key> | commit <marker> | down" ;;
esac
