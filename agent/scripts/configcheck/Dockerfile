# The config check image (design §14.1): Ubuntu 26.04 with Nginx, logrotate,
# systemd and PHP 8.2–8.5 FPM, where check.sh tests the golden files. PHP
# comes from packages.sury.org, set up as §8.2 does it, and the keyring is
# trusted only if its primary key has the fingerprint recorded in Task 1.
FROM ubuntu:26.04

ARG SURY_FINGERPRINT=15058500A0235D97F5D10063B188E2B695BD4743
ARG PHP_VERSIONS="8.2 8.3 8.4 8.5"

RUN set -eux; \
	export DEBIAN_FRONTEND=noninteractive; \
	apt-get update; \
	apt-get install -y --no-install-recommends ca-certificates curl gpg nginx logrotate systemd openssl; \
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
	rm -rf /var/lib/apt/lists/* /tmp/sury.gpg

COPY --chmod=0755 check.sh /usr/local/bin/zimam-configcheck
ENTRYPOINT ["/usr/local/bin/zimam-configcheck"]
