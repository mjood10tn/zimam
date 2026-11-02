#!/usr/bin/env bats
# The whole installer with every command stubbed: the steps run in §4's
# order, and stdout carries only the setup link, as the last line.

load helpers

LINK=https://panel.example.com/setup/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ

setup_file() {
	export KEYS=$BATS_FILE_TMPDIR
	openssl genpkey -algorithm ed25519 -out "$KEYS/key.pem"
	openssl pkey -in "$KEYS/key.pem" -pubout -out "$KEYS/pub.pem"
}

setup() {
	setup_installer
	export LINK
	for cmd in groupadd usermod systemctl; do
		stub_log "$cmd"
	done
	# Like the real ones, these talk on stdout, which must not reach the
	# installer's stdout.
	stub apt-get 'printf "%s\n" "$*" >>"$T/apt-get.log"; echo "Reading package lists..."'
	stub ufw 'printf "%s\n" "$*" >>"$T/ufw.log"; echo "Rules updated"'
	stub useradd 'printf "%s\n" "$*" >>"$T/useradd.log"; mkdir -p "$T/home/zimam"'
	stub id '[[ $1 == -u ]] && echo 0 || exit 1'
	# install -o root -g root -m 0755 <source> <target>
	stub install 'cp "${@: -2:1}" "${@: -1}" && chmod 0755 "${@: -1}"'
	# curl ... -o <file> <url>: serves $T/release by file name.
	stub curl 'while (($#)); do
	case $1 in
	-o) out=$2; shift 2 ;;
	*) url=$1; shift ;;
	esac
done
printf "%s\n" "$url" >>"$T/curl.log"
cp "$T/release/${url##*/}" "$out"'
	release 1.2.3 0
}

# release serves a signed release $1 whose agent prints the link and exits
# with $2. Its version --at-least compares with sort -V, which is enough
# for these versions; the real agent compares in Go.
release() {
	mkdir -p "$T/release"
	cat >"$T/release/zimam-agent-linux-amd64" <<EOF
#!/usr/bin/env bash
printf "%s\n" "\$*" >>"\$T/agent.log"
if [[ \$1 == version ]]; then
	[[ \$(printf "%s\n" "\$3" "$1" | sort -V | head -1) == "\$3" ]]
	exit
fi
echo "bootstrap progress" >&2
[[ $2 == 0 ]] && echo "\$LINK"
exit $2
EOF
	local sum
	sum=$(sha256sum "$T/release/zimam-agent-linux-amd64" | cut -d' ' -f1)
	printf '{"version": "%s", "protocol": 1, "min_agent_version": "%s", "panel_php_min": "8.3", "expires": "%s", "artifacts": {"zimam-agent-linux-amd64": "sha256:%s"}}\n' \
		"$1" "$1" "$(date -u -d '30 days' +%Y-%m-%dT%H:%M:%SZ)" "$sum" >"$T/release/manifest.json"
	openssl pkeyutl -sign -inkey "$KEYS/key.pem" -rawin -in "$T/release/manifest.json" -out "$T/release/manifest.json.sig"
}

install_sh() {
	installer_split 'RELEASE_PUBLIC_KEY_PEM=$(<"$KEYS/pub.pem"); RELEASE_BASE_URL=https://releases.example.com/zimam; main "$@"' "$@"
}

@test "stdout is the setup link alone" {
	install_sh --panel-domain panel.example.com
	echo "$stderr"
	[ "$status" -eq 0 ]
	[ "$output" = "$LINK" ]
	[[ $stderr == *"bootstrap progress"* ]]
	[[ $stderr == *"within 60 minutes"* ]]
}

@test "the latest release is installed and named to bootstrap" {
	install_sh --panel-domain panel.example.com --email admin@example.com
	[ "$status" -eq 0 ]
	[ "$(head -1 "$T/curl.log")" = "https://releases.example.com/zimam/latest/download/manifest.json" ]
	grep -qx "https://releases.example.com/zimam/latest/download/zimam-agent-linux-amd64" "$T/curl.log"
	cmp "$T/zimam-agent" "$T/release/zimam-agent-linux-amd64"
	[ "$(cat "$T/agent.log")" = "bootstrap --panel-domain panel.example.com --version 1.2.3 --email admin@example.com" ]
}

@test "--version downloads that release" {
	install_sh --panel-domain panel.example.com --version 1.2.3
	[ "$status" -eq 0 ]
	[ "$(head -1 "$T/curl.log")" = "https://releases.example.com/zimam/download/v1.2.3/manifest.json" ]
}

@test "a manifest of another release than --version fails" {
	install_sh --panel-domain panel.example.com --version 1.2.4
	[ "$status" -eq 1 ]
	[[ $stderr == *"release 1.2.3, not 1.2.4"* ]]
	[ ! -e "$T/zimam-agent" ]
}

# installed puts an agent on the server that reports version $1, or, with
# no argument, an agent too old to have the version subcommand.
installed() {
	if [[ -n ${1:-} ]]; then
		printf '#!/usr/bin/env bash\n[[ $1 == version ]] && echo %s\n' "$1" >"$T/zimam-agent"
	else
		printf '#!/usr/bin/env bash\nexit 2\n' >"$T/zimam-agent"
	fi
	chmod 0755 "$T/zimam-agent"
}

# A repair run never moves a server to an older release (M1 review, M5): the
# verified new agent compares its version with the installed one's.
@test "a repair run refuses a release older than the installed agent" {
	installed 1.3.0
	install_sh --panel-domain panel.example.com
	[ "$status" -eq 1 ]
	[[ $stderr == *"runs zimam-agent 1.3.0, which is newer than release 1.2.3"* ]]
	[[ $stderr == *"--version 1.3.0"* ]]
	grep -qx "version --at-least 1.3.0" "$T/agent.log"
	run ! grep -q bootstrap "$T/agent.log"
	[ "$("$T/zimam-agent" version)" = 1.3.0 ]
}

@test "a repair run with the same or a newer release goes ahead" {
	for v in 1.2.3 1.0.0; do
		installed "$v"
		rm -f "$T/agent.log"
		install_sh --panel-domain panel.example.com
		[ "$status" -eq 0 ] || {
			echo "installed $v: $stderr"
			return 1
		}
		grep -qx "version --at-least $v" "$T/agent.log"
		cmp "$T/zimam-agent" "$T/release/zimam-agent-linux-amd64"
	done
}

@test "an installed agent without the version subcommand doesn't block a repair" {
	installed
	install_sh --panel-domain panel.example.com
	[ "$status" -eq 0 ]
	run ! grep -q "^version" "$T/agent.log"
}

@test "the steps run in order" {
	install_sh --panel-domain panel.example.com
	[ "$status" -eq 0 ]
	[ -f "$T/etc-zimam/installer.state" ]
	# apt-get waits for a dpkg lock that cloud-init or unattended-upgrades
	# holds on a server that has just booted.
	[ "$(head -2 "$T/apt-get.log")" = "$(printf '%s\n' "-o DPkg::Lock::Timeout=300 update -q" \
		"-o DPkg::Lock::Timeout=300 install -y -q nginx certbot ufw curl ca-certificates openssl")" ]
	[ "$(cat "$T/groupadd.log")" = "--system zimam-agent" ]
	[ "$(cat "$T/useradd.log")" = "--system --create-home --home-dir $T/home/zimam --shell /usr/sbin/nologin --user-group --groups zimam-agent zimam" ]
	# zimam's groups are set, not added to, so a re-run takes away sudo.
	[ "$(cat "$T/usermod.log")" = "$(printf '%s\n' "--groups zimam-agent --shell /usr/sbin/nologin zimam" "--append --groups zimam www-data")" ]
	[ "$(stat -c %a "$T/home/zimam")" = 750 ]
	cmp "$T/units/zimam-agent.socket" "$FIXTURES/zimam-agent.socket"
	grep -qx "enable --now zimam-agent.socket" "$T/systemctl.log"
}

@test "the firewall allows SSH, HTTP and HTTPS, then is enabled" {
	stub sshd 'printf "port 22\nport 2222\n"'
	install_sh --panel-domain panel.example.com
	[ "$status" -eq 0 ]
	[ "$(cat "$T/ufw.log")" = "$(printf 'allow 22/tcp\nallow 2222/tcp\nallow 80/tcp\nallow 443/tcp\n--force enable')" ]
}

@test "--skip-firewall configures UFW without enabling it" {
	install_sh --panel-domain panel.example.com --skip-firewall
	[ "$status" -eq 0 ]
	run ! grep -q enable "$T/ufw.log"
	grep -qx "allow 443/tcp" "$T/ufw.log"
}

@test "a failed bootstrap fails the installer, with nothing on stdout" {
	release 1.2.3 1
	install_sh --panel-domain panel.example.com
	[ "$status" -eq 1 ]
	[ -z "$output" ]
	[[ $stderr == *"run the installer again"* ]]
}

@test "a failed preflight changes nothing" {
	os_release ubuntu 24.04
	install_sh --panel-domain panel.example.com
	[ "$status" -eq 1 ]
	[ ! -e "$T/apt-get.log" ]
	[ ! -e "$T/curl.log" ]
}
