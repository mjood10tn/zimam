# The config check image (design §14.1): Ubuntu 26.04 with Nginx, logrotate,
# systemd, MySQL, Valkey and PHP 8.2–8.5 FPM, where check.sh tests the golden
# files. PHP comes from packages.sury.org, set up as §8.2 does it, and the keyring is
# trusted only if its primary key has the fingerprint the agent checks
# (php.SuryFingerprint). The NodeSource keyring is there too, dearmored and
# checked against node.Fingerprint, for the apt-node-* golden cases. MySQL and
# Valkey come from Ubuntu main. rclone is the release the agent pins
# (internal/rclone, R05-D1), checked against the same zip hashes; a test
# there keeps the ARGs below in step with the pin.
FROM ubuntu:26.04

ARG SURY_FINGERPRINT=15058500A0235D97F5D10063B188E2B695BD4743
ARG NODE_FINGERPRINT=6F71F525282841EEDAF851B42F59B5F99B1BE0B4
ARG PHP_VERSIONS="8.2 8.3 8.4 8.5"
ARG RCLONE_VERSION=1.76.0
ARG RCLONE_AMD64_SHA256=b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7
ARG RCLONE_ARM64_SHA256=ee169016675e23a69d77240565133ff7dbc9b33d16fa8f7053d4af39f1eaeb78

RUN set -eux; \
	export DEBIAN_FRONTEND=noninteractive; \
	apt-get update; \
	apt-get install -y --no-install-recommends ca-certificates curl gpg nginx logrotate systemd openssl mysql-server valkey-server unzip; \
	curl -fsSL https://packages.sury.org/php/apt.gpg -o /tmp/sury.gpg; \
	fingerprint="$(gpg --show-keys --with-colons /tmp/sury.gpg | awk -F: '$1 == "fpr" { print $10; exit }')"; \
	test "$fingerprint" = "$SURY_FINGERPRINT"; \
	install -D -m 0644 /tmp/sury.gpg /etc/apt/keyrings/zimam-sury.gpg; \
	printf '%s\n' 'Types: deb' 'URIs: https://packages.sury.org/php/' 'Suites: resolute' 'Components: main' \
		'Signed-By: /etc/apt/keyrings/zimam-sury.gpg' > /etc/apt/sources.list.d/zimam-sury.sources; \
	printf '%s\n' 'Package: php*' 'Pin: origin "packages.sury.org"' 'Pin-Priority: 900' > /etc/apt/preferences.d/zimam-sury.pref; \
	apt-get update; \
	packages=""; \
	for v in $PHP_VERSIONS; do packages="$packages php$v-fpm"; done; \
	apt-get install -y --no-install-recommends $packages; \
	curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key -o /tmp/nodesource.key; \
	fingerprint="$(gpg --show-keys --with-colons /tmp/nodesource.key | awk -F: '$1 == "fpr" { print $10; exit }')"; \
	test "$fingerprint" = "$NODE_FINGERPRINT"; \
	gpg --dearmor < /tmp/nodesource.key > /etc/apt/keyrings/zimam-nodesource.gpg; \
	arch="$(dpkg --print-architecture)"; \
	case "$arch" in amd64) rclone_sha="$RCLONE_AMD64_SHA256" ;; arm64) rclone_sha="$RCLONE_ARM64_SHA256" ;; *) exit 1 ;; esac; \
	curl -fsSL "https://downloads.rclone.org/v$RCLONE_VERSION/rclone-v$RCLONE_VERSION-linux-$arch.zip" -o /tmp/rclone.zip; \
	echo "$rclone_sha  /tmp/rclone.zip" | sha256sum -c -; \
	unzip -p /tmp/rclone.zip "rclone-v$RCLONE_VERSION-linux-$arch/rclone" > /usr/local/bin/rclone; \
	chmod 0755 /usr/local/bin/rclone; \
	rm -rf /var/lib/apt/lists/* /tmp/sury.gpg /tmp/nodesource.key /tmp/rclone.zip

COPY --chmod=0755 check.sh /usr/local/bin/zimam-configcheck
ENTRYPOINT ["/usr/local/bin/zimam-configcheck"]
