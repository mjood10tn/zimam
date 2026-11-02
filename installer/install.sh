#!/usr/bin/env bash
# Zimam's installer (design §4). On a clean Ubuntu 26.04 server, as root:
#
#   curl -fsSL <release-base-url>/latest/download/install.sh | sudo bash -s -- --panel-domain panel.example.com
#
# It does only what must happen before the agent exists, then hands over to
# `zimam-agent bootstrap`, which prints the setup link. Every step checks
# before it acts, so running the installer again repairs an install. Progress
# goes to stderr; stdout gets the setup link alone, as the last line.
set -Eeuo pipefail

# Filled in by .github/scripts/build-release.sh.
RELEASE_PUBLIC_KEY_PEM='@@RELEASE_PUBLIC_KEY_PEM@@'
RELEASE_BASE_URL='@@RELEASE_BASE_URL@@'

# What the installer reads and writes. The tests point these elsewhere.
OS_RELEASE=/etc/os-release
MEMINFO=/proc/meminfo
CONFIG_DIR=/etc/zimam
UNIT_DIR=/etc/systemd/system
AGENT_BIN=/usr/local/sbin/zimam-agent
ZIMAM_HOME=/home/zimam

# MemTotal leaves out what the kernel reserves, so a 2 GiB server reports
# about 1.9 GiB and a 4 GiB one about 3.8 GiB.
MIN_RAM_KB=$((1800 * 1024))
WARN_RAM_KB=$((3600 * 1024))
MIN_DISK_BYTES=$((15 * 1024 * 1024 * 1024))

PANEL_DOMAIN=
EMAIL=
PHP=
VERSION=
ACME_SERVER=
ACME_CA_BUNDLE=
SKIP_FIREWALL=0
YES=0

ARCH=
SSH_PORTS=()
RELEASE_VERSION=
BOOTSTRAP_ARGS=()
SETUP_LINK=
WORK_DIR=

log() { printf '==> %s\n' "$*" >&2; }
warn() { printf 'Warning: %s\n' "$*" >&2; }
die() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat >&2 <<'EOF'
Usage: install.sh --panel-domain <fqdn> [options]

  --panel-domain <fqdn>    The panel's domain. It must already point at this server.
  --email <address>        Email for the ACME account.
  --php <x.y>              Default PHP version for sites and the php command.
  --version <x.y.z>        Release to install. Defaults to the latest.
  --acme-server <url>      Use a test ACME server, such as Pebble.
  --acme-ca-bundle <path>  Trust this CA bundle for the ACME server.
  --skip-firewall          Configure UFW but don't enable it (containers).
  --yes                    Never prompt.
EOF
}

usage_error() {
	printf 'Error: %s\n' "$*" >&2
	usage
	exit 2
}

# parse_args reads §4's flags. The values reach bootstrap, which checks them
# again; these checks catch typing mistakes before anything is installed.
parse_args() {
	while (($#)); do
		case $1 in
		--panel-domain | --email | --php | --version | --acme-server | --acme-ca-bundle)
			(($# >= 2)) || usage_error "$1 needs a value"
			case $1 in
			--panel-domain) PANEL_DOMAIN=$2 ;;
			--email) EMAIL=$2 ;;
			--php) PHP=$2 ;;
			--version) VERSION=$2 ;;
			--acme-server) ACME_SERVER=$2 ;;
			--acme-ca-bundle) ACME_CA_BUNDLE=$2 ;;
			esac
			shift 2
			;;
		--skip-firewall)
			SKIP_FIREWALL=1
			shift
			;;
		--yes)
			YES=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) usage_error "unknown option $1" ;;
		esac
	done
	[[ -n $PANEL_DOMAIN ]] || usage_error "--panel-domain is required"
	[[ $PANEL_DOMAIN =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$ ]] ||
		usage_error "--panel-domain $PANEL_DOMAIN isn't a domain name"
	[[ -z $EMAIL || $EMAIL =~ ^[^[:space:]@]+@[^[:space:]@]+$ ]] || usage_error "--email $EMAIL isn't an email address"
	[[ -z $PHP || $PHP =~ ^[0-9]+\.[0-9]+$ ]] || usage_error "--php $PHP isn't a PHP version such as 8.5"
	[[ -z $VERSION || $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
		usage_error "--version $VERSION isn't a release version such as 1.2.3"
	[[ -z $ACME_SERVER || $ACME_SERVER =~ ^https://[^[:space:]]+$ ]] || usage_error "--acme-server $ACME_SERVER isn't an https URL"
	[[ -z $ACME_CA_BUNDLE || $ACME_CA_BUNDLE == /* ]] || usage_error "--acme-ca-bundle $ACME_CA_BUNDLE isn't an absolute path"
}

# Step 1: preflight.

preflight() {
	log "Checking this server"
	check_os
	check_arch
	check_root
	check_clean
	check_memory
	check_disk
	collect_ssh_ports
	if ((${#SSH_PORTS[@]} == 0)); then
		((SKIP_FIREWALL)) ||
			die "No SSH port found (sshd -T printed none), so enabling the firewall could lock you out. Check sshd, or use --skip-firewall."
		warn "No SSH port found; the firewall stays off (--skip-firewall)."
	fi
	[[ -z $ACME_CA_BUNDLE || -f $ACME_CA_BUNDLE ]] || die "The ACME CA bundle $ACME_CA_BUNDLE doesn't exist."
	write_state
}

# os_field reads one field of os-release without running it.
os_field() {
	local line
	while IFS= read -r line; do
		if [[ $line == "$1="* ]]; then
			line=${line#"$1="}
			line=${line#\"}
			printf '%s\n' "${line%\"}"
			return
		fi
	done <"$OS_RELEASE"
}

check_os() {
	local id version
	id=$(os_field ID)
	version=$(os_field VERSION_ID)
	[[ $id == ubuntu ]] || die "Zimam supports only Ubuntu 26.04, and this is ${id:-an unknown system}."
	[[ $version == 26.04 ]] || die "Zimam supports only Ubuntu 26.04, and this is Ubuntu $version."
}

check_arch() {
	local machine
	machine=$(uname -m)
	case $machine in
	x86_64) ARCH=amd64 ;;
	aarch64) ARCH=arm64 ;;
	*) die "Zimam supports amd64 or arm64 servers, and this one is $machine." ;;
	esac
}

check_root() {
	[[ $(id -u) == 0 ]] || die "Run the installer as root, for example with sudo."
}

# check_clean refuses a server that already runs a web server, PHP or a
# database, or has a user named zimam, unless an earlier Zimam run left its
# marker: then this is a repair (D17).
check_clean() {
	[[ -e $CONFIG_DIR/agent.json || -e $CONFIG_DIR/installer.state ]] && return 0
	local installed
	installed=$(dpkg-query -W -f='${db:Status-Status} ${Package}\n' nginx apache2 'php*' mysql-server mariadb-server 2>/dev/null |
		awk '$1 == "installed" { printf "%s%s", sep, $2; sep = ", " }' || true)
	[[ -z $installed ]] ||
		die "This server isn't clean: $installed is already installed. Zimam needs a fresh Ubuntu 26.04 server."
	if getent passwd zimam >/dev/null; then
		die "This server already has a user zimam, which Zimam keeps for its panel."
	fi
}

check_memory() {
	local kb
	kb=$(awk '$1 == "MemTotal:" { print $2 }' "$MEMINFO")
	[[ $kb =~ ^[0-9]+$ ]] || die "Couldn't read MemTotal from $MEMINFO."
	((kb >= MIN_RAM_KB)) || die "Zimam needs at least 2 GiB of RAM, and this server has $((kb / 1024)) MiB."
	((kb >= WARN_RAM_KB)) || warn "This server has $((kb / 1024)) MiB of RAM; 4 GiB or more is recommended."
}

check_disk() {
	local avail
	avail=$(df --output=avail -B1 / | tail -n 1)
	avail=${avail//[[:space:]]/}
	[[ $avail =~ ^[0-9]+$ ]] || die "Couldn't read the free space on /."
	((avail >= MIN_DISK_BYTES)) ||
		die "Zimam needs 15 GiB free on /, and this server has $((avail / 1024 / 1024 / 1024)) GiB."
}

# collect_ssh_ports finds the ports sshd is configured for (sshd -T), and
# the ones ssh.socket listens on: after an edit of Port without a restart of
# ssh.socket the two differ, and the firewall must allow both.
collect_ssh_ports() {
	SSH_PORTS=()
	local key value port
	while read -r key value; do
		[[ $key == port && $value =~ ^[0-9]+$ ]] && add_ssh_port "$value"
	done < <(sshd -T 2>/dev/null || true)
	while read -r port; do
		add_ssh_port "$port"
	done < <(systemctl show ssh.socket --property=Listen --value 2>/dev/null |
		grep -oE ':[0-9]+ \(Stream\)' | grep -oE '[0-9]+' || true)
}

add_ssh_port() {
	local p
	for p in "${SSH_PORTS[@]}"; do
		[[ $p == "$1" ]] && return
	done
	SSH_PORTS+=("$1")
}

# write_state marks the server as a Zimam install, so a run that stops after
# installing Nginx can be repeated (D17).
write_state() {
	mkdir -p "$CONFIG_DIR"
	chmod 0755 "$CONFIG_DIR"
	(
		umask 077
		printf 'preflight_passed=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$CONFIG_DIR/installer.state.tmp"
	)
	chmod 0600 "$CONFIG_DIR/installer.state.tmp"
	mv -f "$CONFIG_DIR/installer.state.tmp" "$CONFIG_DIR/installer.state"
}

# Step 2: packages from Ubuntu.

install_packages() {
	log "Installing nginx, certbot, ufw, curl, ca-certificates and openssl"
	DEBIAN_FRONTEND=noninteractive apt-get update -q
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q nginx certbot ufw curl ca-certificates openssl
}

# Step 3: the accounts (§4, §5.1). Only zimam is in zimam-agent; www-data
# joins zimam to serve the panel's public files.

create_accounts() {
	log "Creating the zimam user"
	getent group zimam-agent >/dev/null || groupadd --system zimam-agent
	if ! id zimam >/dev/null 2>&1; then
		useradd --system --create-home --home-dir "$ZIMAM_HOME" --shell /usr/sbin/nologin --user-group --groups zimam-agent zimam
	fi
	# Converge a user from an earlier run: these groups only, so no sudo.
	usermod --groups zimam-agent --shell /usr/sbin/nologin zimam
	chmod 0750 "$ZIMAM_HOME"
	usermod --append --groups zimam www-data
}

# Step 4: the agent, from a verified release.

release_url() {
	if [[ -n $VERSION ]]; then
		printf '%s/download/v%s\n' "$RELEASE_BASE_URL" "$VERSION"
	else
		printf '%s/latest/download\n' "$RELEASE_BASE_URL"
	fi
}

fetch() {
	curl -fsSL --retry 3 -o "$2" "$1" || die "Couldn't download $1."
}

# Verify the manifest's signature over its exact bytes, then its expiry,
# then the agent binary's digest. Nothing read from the manifest is used
# before the signature check passes.
verify_release() {
	local dir=$1 arch=$2 key expires want got
	key="$dir/release-key.pem"
	printf '%s\n' "$RELEASE_PUBLIC_KEY_PEM" >"$key"
	openssl pkeyutl -verify -pubin -inkey "$key" -rawin \
		-in "$dir/manifest.json" -sigfile "$dir/manifest.json.sig" >/dev/null 2>&1 ||
		die "The release manifest's signature is not valid."
	expires=$(manifest_field "$dir/manifest.json" expires)
	[[ $(date -u +%s) -lt $(date -u -d "$expires" +%s) ]] ||
		die "The release manifest expired on $expires."
	want=$(manifest_field "$dir/manifest.json" artifacts "zimam-agent-linux-$arch" 2>/dev/null) ||
		die "The release manifest doesn't list zimam-agent-linux-$arch."
	got="sha256:$(sha256sum "$dir/zimam-agent-linux-$arch" | cut -d' ' -f1)"
	[[ $got == "$want" ]] || die "The agent binary doesn't match the signed manifest."
}

# manifest_field reads one value with Python's JSON parser. certbot,
# installed in step 2, depends on python3 (Task 1). -I ignores the
# environment and the current directory.
manifest_field() {
	python3 -I - "$@" <<'PY'
import json, sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2:]:
    value = value[key]
print(value)
PY
}

install_agent() {
	local url
	url=$(release_url)
	log "Downloading the release from $url"
	fetch "$url/manifest.json" "$WORK_DIR/manifest.json"
	fetch "$url/manifest.json.sig" "$WORK_DIR/manifest.json.sig"
	fetch "$url/zimam-agent-linux-$ARCH" "$WORK_DIR/zimam-agent-linux-$ARCH"
	verify_release "$WORK_DIR" "$ARCH"
	RELEASE_VERSION=$(manifest_field "$WORK_DIR/manifest.json" version)
	[[ $RELEASE_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
		die "The release manifest names the version $RELEASE_VERSION."
	[[ -z $VERSION || $RELEASE_VERSION == "$VERSION" ]] ||
		die "The manifest at $url is for release $RELEASE_VERSION, not $VERSION."
	log "Installing zimam-agent $RELEASE_VERSION"
	install -o root -g root -m 0755 "$WORK_DIR/zimam-agent-linux-$ARCH" "$AGENT_BIN.zimam-new"
	mv -f "$AGENT_BIN.zimam-new" "$AGENT_BIN"
}

# Step 5: the agent's socket and service (§5.1).

socket_unit() {
	cat <<'EOF'
# /etc/systemd/system/zimam-agent.socket
[Socket]
ListenStream=/run/zimam/agent.sock
SocketUser=root
SocketGroup=zimam-agent
SocketMode=0660

[Install]
WantedBy=sockets.target
EOF
}

service_unit() {
	cat <<'EOF'
# /etc/systemd/system/zimam-agent.service
[Unit]
Requires=zimam-agent.socket
After=zimam-agent.socket

[Service]
ExecStart=/usr/local/sbin/zimam-agent serve
Restart=on-failure
UMask=0077
EOF
}

# write_unit replaces a unit file only when its content differs.
write_unit() {
	local path=$UNIT_DIR/$1 content=$2
	if [[ -f $path && "$(<"$path")" == "$content" ]]; then
		return
	fi
	printf '%s\n' "$content" >"$path.zimam-new"
	chmod 0644 "$path.zimam-new"
	mv -f "$path.zimam-new" "$path"
}

write_units() {
	log "Starting the agent's socket"
	write_unit zimam-agent.socket "$(socket_unit)"
	write_unit zimam-agent.service "$(service_unit)"
	systemctl daemon-reload
	systemctl enable --now zimam-agent.socket
	# A re-run that brought a new agent restarts a running one.
	systemctl try-restart zimam-agent.service
}

# Step 6: the firewall.

configure_firewall() {
	local port
	log "Allowing SSH (${SSH_PORTS[*]:-none found}), HTTP and HTTPS in UFW"
	for port in "${SSH_PORTS[@]}"; do
		ufw allow "$port/tcp"
	done
	ufw allow 80/tcp
	ufw allow 443/tcp
	if ((SKIP_FIREWALL)); then
		log "Leaving UFW off (--skip-firewall)"
	else
		ufw --force enable
	fi
}

# Step 7: bootstrap, from the release just verified.

bootstrap_args() {
	BOOTSTRAP_ARGS=(--panel-domain "$PANEL_DOMAIN" --version "$RELEASE_VERSION")
	((YES == 0)) || BOOTSTRAP_ARGS+=(--yes)
	[[ -z $EMAIL ]] || BOOTSTRAP_ARGS+=(--email "$EMAIL")
	[[ -z $PHP ]] || BOOTSTRAP_ARGS+=(--php "$PHP")
	[[ -z $ACME_SERVER ]] || BOOTSTRAP_ARGS+=(--acme-server "$ACME_SERVER")
	[[ -z $ACME_CA_BUNDLE ]] || BOOTSTRAP_ARGS+=(--acme-ca-bundle "$ACME_CA_BUNDLE")
}

run_bootstrap() {
	bootstrap_args
	log "Running zimam-agent bootstrap"
	SETUP_LINK=$("$AGENT_BIN" bootstrap "${BOOTSTRAP_ARGS[@]}") ||
		die "Bootstrap stopped. Fix the problem above, then run the installer again: finished steps are skipped."
}

main() {
	parse_args "$@"
	WORK_DIR=$(mktemp -d)
	trap 'rm -rf "$WORK_DIR"' EXIT
	trap 'printf "Error: the installer stopped at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
	{
		preflight
		install_packages
		create_accounts
		install_agent
		write_units
		configure_firewall
		run_bootstrap
	} >&2
	log "Zimam is installed. Open this link within 60 minutes to create the panel's admin account:"
	printf '%s\n' "$SETUP_LINK"
}

[[ ${ZIMAM_INSTALL_SOURCE_ONLY:-} == 1 ]] || main "$@"
