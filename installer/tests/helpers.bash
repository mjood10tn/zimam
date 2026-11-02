# Shared setup for the installer's bats tests. Each test runs install.sh's
# functions in a fresh bash that has sourced it, so they run as they do on
# a server (set -Eeuo pipefail). Commands are stubbed first on PATH, and
# the files the installer reads and writes are pointed into the test's
# temporary directory.

bats_require_minimum_version 1.5.0

INSTALLER=$BATS_TEST_DIRNAME/../install.sh
FIXTURES=$BATS_TEST_DIRNAME/fixtures

# setup_installer makes a clean Ubuntu 26.04 amd64 server with 8 GiB of
# RAM, 100 GiB free and SSH on port 22, on which the installer runs as root.
setup_installer() {
	T=$BATS_TEST_TMPDIR
	STUBS=$T/bin
	mkdir -p "$STUBS" "$T/etc-zimam" "$T/units" "$T/home"
	export T STUBS INSTALLER FIXTURES
	export PATH="$STUBS:$PATH"
	os_release ubuntu 26.04
	meminfo $((8 * 1024 * 1024))
	stub uname 'echo x86_64'
	stub id '[[ $1 == -u ]] && echo 0 || exit 1'
	stub getent 'exit 2'
	stub dpkg-query 'exit 1'
	stub df 'printf "         Avail\n%s\n" $((100 * 1024 ** 3))'
	stub sshd 'printf "port 22\naddressfamily any\n"'
	stub systemctl 'exit 0'
}

# stub writes a command that runs body, first on PATH.
stub() {
	printf '#!/usr/bin/env bash\n%s\n' "$2" >"$STUBS/$1"
	chmod +x "$STUBS/$1"
}

# stub_log writes a command that appends its arguments to $T/<name>.log.
stub_log() {
	stub "$1" "printf '%s\n' \"\$*\" >>\"\$T/$1.log\""
}

os_release() {
	printf 'NAME="Ubuntu"\nID=%s\nVERSION_ID="%s"\n' "$1" "$2" >"$T/os-release"
}

meminfo() {
	printf 'MemTotal:       %s kB\nMemFree:          100000 kB\n' "$1" >"$T/meminfo"
}

# installer runs script in a fresh bash that has sourced install.sh, with
# its paths under $T. The arguments after script are the script's "$@".
installer() {
	run bash -c "$(installer_prelude)"'
script=$1
shift
eval "$script"' installer-test "$@"
}

# installer_split is installer with stdout in $output and stderr in $stderr.
installer_split() {
	run --separate-stderr bash -c "$(installer_prelude)"'
script=$1
shift
eval "$script"' installer-test "$@"
}

installer_prelude() {
	cat <<'EOF'
ZIMAM_INSTALL_SOURCE_ONLY=1 source "$INSTALLER"
OS_RELEASE=$T/os-release
MEMINFO=$T/meminfo
CONFIG_DIR=$T/etc-zimam
UNIT_DIR=$T/units
AGENT_BIN=$T/zimam-agent
ZIMAM_HOME=$T/home/zimam
EOF
}
