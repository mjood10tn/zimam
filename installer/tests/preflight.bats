#!/usr/bin/env bats
# Step 1 of design §4: preflight, and the installer.state marker (D17).

load helpers

setup() {
	setup_installer
}

@test "a clean Ubuntu 26.04 server passes and gets the marker" {
	installer preflight
	[ "$status" -eq 0 ]
	[ -f "$T/etc-zimam/installer.state" ]
	[ "$(stat -c %a "$T/etc-zimam/installer.state")" = 600 ]
}

@test "a system that isn't Ubuntu fails" {
	os_release debian 13
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"only Ubuntu 26.04"* ]]
	[ ! -e "$T/etc-zimam/installer.state" ]
}

@test "Ubuntu 24.04 fails" {
	os_release ubuntu 24.04
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"Ubuntu 24.04"* ]]
}

@test "i386 and riscv64 fail" {
	for machine in i686 riscv64; do
		stub uname "echo $machine"
		installer preflight
		[ "$status" -eq 1 ]
		[[ $output == *"amd64 or arm64"* ]]
	done
}

@test "arm64 passes" {
	stub uname 'echo aarch64'
	installer 'preflight; echo "arch=$ARCH"'
	[ "$status" -eq 0 ]
	[[ $output == *"arch=arm64"* ]]
}

@test "not root fails" {
	stub id '[[ $1 == -u ]] && echo 1000'
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"as root"* ]]
}

@test "Nginx installed with no marker fails" {
	stub dpkg-query 'printf "installed nginx\nnot-installed apache2\n"'
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"isn't clean"*"nginx"* ]]
	[[ $output != *apache2* ]]
	[ ! -e "$T/etc-zimam/installer.state" ]
}

@test "Nginx installed with the marker passes" {
	stub dpkg-query 'printf "installed nginx\n"'
	touch "$T/etc-zimam/installer.state"
	installer preflight
	[ "$status" -eq 0 ]
}

@test "Nginx installed with agent.json passes" {
	stub dpkg-query 'printf "installed nginx\n"'
	touch "$T/etc-zimam/agent.json"
	installer preflight
	[ "$status" -eq 0 ]
}

@test "an existing zimam user with no marker fails" {
	stub getent '[[ $1 == passwd && $2 == zimam ]] && echo "zimam:x:1000:1000::/home/zimam:/bin/bash"'
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"user zimam"* ]]
}

@test "1.5 GiB of RAM fails" {
	meminfo $((1536 * 1024))
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"2 GiB of RAM"* ]]
}

@test "a 2 GiB server, which reports a little less, passes" {
	meminfo 1961000
	installer preflight
	[ "$status" -eq 0 ]
}

@test "3 GiB of RAM warns" {
	meminfo $((3 * 1024 * 1024))
	installer preflight
	[ "$status" -eq 0 ]
	[[ $output == *"Warning"*"4 GiB"* ]]
}

@test "8 GiB of RAM doesn't warn" {
	installer preflight
	[ "$status" -eq 0 ]
	[[ $output != *Warning* ]]
}

@test "10 GiB of free disk fails" {
	stub df 'printf "         Avail\n%s\n" $((10 * 1024 ** 3))'
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"15 GiB"* ]]
}

@test "SSH ports 22 and 2222 are both found" {
	stub sshd 'printf "port 22\nport 2222\nlistenaddress [::]:2222\n"'
	installer 'collect_ssh_ports; echo "ports=${SSH_PORTS[*]}"'
	[ "$status" -eq 0 ]
	[[ $output == *"ports=22 2222" ]]
}

@test "a port ssh.socket listens on is found as well" {
	stub systemctl '[[ "$*" == "show ssh.socket --property=Listen --value" ]] && printf "[::]:2200 (Stream)\n0.0.0.0:22 (Stream)\n"'
	installer 'collect_ssh_ports; echo "ports=${SSH_PORTS[*]}"'
	[ "$status" -eq 0 ]
	[[ $output == *"ports=22 2200" ]]
}

@test "no SSH port with the firewall to be enabled fails" {
	stub sshd 'exit 255'
	installer preflight
	[ "$status" -eq 1 ]
	[[ $output == *"SSH"*"--skip-firewall"* ]]
	[ ! -e "$T/etc-zimam/installer.state" ]
}

@test "no SSH port with --skip-firewall passes" {
	stub sshd 'exit 255'
	installer 'SKIP_FIREWALL=1; preflight'
	[ "$status" -eq 0 ]
}

@test "a missing ACME CA bundle fails" {
	installer 'ACME_CA_BUNDLE=$T/none.pem; preflight'
	[ "$status" -eq 1 ]
	[[ $output == *"none.pem"* ]]
}
