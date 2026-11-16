#!/usr/bin/env bash
# Runs inside the Git host (githost.sh pushes it to /root/githost-server.sh),
# as root. The Git host plays GitHub: OpenSSH, and a user git whose shell is
# git-shell, so a deploy key can fetch and run nothing else. The repository
# is ~git/zimam-e2e/laravel-app.git, the path git@github.com:zimam-e2e/
# laravel-app.git names; ~git/work is the clone the scenario commits in.
#   server.sh setup             packages, the user, sshd with a host key made
#                               here, and the repository seeded from
#                               /root/fixture; prints the ed25519 host key
#   server.sh authorize <key>   let one OpenSSH public key fetch
#   server.sh commit <marker>   release.txt = <marker>, committed and pushed
#                               to main; prints the commit
set -Eeuo pipefail
cd /

repo=/home/git/zimam-e2e/laravel-app.git
work=/home/git/work
as_git() { runuser -u git -- "$@"; }
git_in_work() {
	as_git git -C "$work" -c user.name='Zimam e2e' -c user.email=e2e@zimam.test -c commit.gpgsign=false "$@"
}

case ${1:-} in
setup)
	if ! type -P sshd git git-shell >/dev/null; then
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -y -qq --no-install-recommends openssh-server git >/dev/null
	fi
	id git >/dev/null 2>&1 || useradd --create-home --shell "$(type -P git-shell)" git
	install -d -o git -g git -m 0700 /home/git/.ssh
	install -o git -g git -m 0600 /dev/null /home/git/.ssh/authorized_keys
	# A host key made now: it lives only as long as this Git host.
	ssh-keygen -A >/dev/null
	if [[ -d /run/systemd/system ]]; then
		systemctl enable --now ssh.socket >/dev/null 2>&1 || systemctl restart ssh
	else
		# Docker: no systemd, so sshd runs by itself.
		mkdir -p /run/sshd
		/usr/sbin/sshd
	fi
	rm -rf "$repo" "$work"
	as_git mkdir -p "$(dirname "$repo")"
	as_git git init -q --bare -b main "$repo"
	cp -a /root/fixture "$work"
	chown -R git:git "$work"
	as_git git init -q -b main "$work"
	git_in_work add -A
	git_in_work commit -q -m "The e2e Laravel app, $(cat "$work/release.txt")"
	git_in_work remote add origin "$repo"
	git_in_work push -q origin main
	cut -d ' ' -f 1,2 /etc/ssh/ssh_host_ed25519_key.pub
	;;
authorize)
	key=${2:-}
	[[ $key =~ ^ssh-ed25519\ [A-Za-z0-9+/]+=*$ ]] || {
		echo "server.sh: not an ssh-ed25519 public key: $key" >&2
		exit 1
	}
	# restrict: no forwarding, no terminal; git-shell runs only git's own commands.
	grep -qxF "restrict $key" /home/git/.ssh/authorized_keys || echo "restrict $key" >>/home/git/.ssh/authorized_keys
	;;
commit)
	marker=${2:?usage: server.sh commit <marker>}
	echo "$marker" | as_git tee "$work/release.txt" >/dev/null
	git_in_work commit -q -am "Release $marker"
	git_in_work push -q origin main
	git_in_work rev-parse HEAD
	;;
*)
	echo "usage: server.sh setup | authorize <key> | commit <marker>" >&2
	exit 2
	;;
esac
