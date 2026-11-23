#!/usr/bin/env bash
# Runs inside the sinks container (sinks.sh pushes it to /root/sinks-server.sh),
# as root. The places v0.5.0's backups and notifications send to:
#   - S3: `rclone serve s3` over HTTPS with a certificate from a CA made
#     here, backed by /srv/s3 (each top folder is a bucket); the rclone
#     release is the one the agent pins, checked by its zip's SHA-256;
#   - SFTP: OpenSSH with a user sftpbackup (Ubuntu has a system user
#     backup already), whose authorized_keys the scenario fills with the
#     agent's public key (the owner's step);
#   - SMTP: smtp-sink.py on port 2525, every message a file in /srv/mail.
#   server.sh setup <s3 name> <access key> <secret key> <bucket> <rclone version> <zip sha256>
#   server.sh authorize <key>   let one OpenSSH public key log in as sftpbackup
#   server.sh mail              print every message received, oldest first
set -Eeuo pipefail
cd /

case ${1:-} in
setup)
	name=$2 access=$3 secret=$4 bucket=$5 version=$6 sum=$7
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y -qq --no-install-recommends openssh-server openssl ca-certificates curl unzip python3 >/dev/null
	arch=$(dpkg --print-architecture)
	zip=/root/rclone.zip
	curl -fsSL -o "$zip" "https://downloads.rclone.org/v$version/rclone-v$version-linux-$arch.zip"
	echo "$sum  $zip" | sha256sum -c --quiet
	unzip -q -o -j "$zip" "rclone-v$version-linux-$arch/rclone" -d /usr/local/bin
	chmod 0755 /usr/local/bin/rclone

	# A CA and the S3 name's certificate, made now and gone with the container.
	install -d -m 0700 /root/tls
	cd /root/tls
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj "/CN=Zimam e2e sinks CA" \
		-keyout ca.key -out ca.pem 2>/dev/null
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj "/CN=$name" -keyout s3.key -out s3.csr 2>/dev/null
	printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' "$name" >s3.ext
	openssl x509 -req -in s3.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 2 -extfile s3.ext -out s3.pem 2>/dev/null
	cd /
	install -d /srv/s3 "/srv/s3/$bucket" /srv/mail
	systemctl stop e2e-s3 e2e-smtp 2>/dev/null || true
	systemctl reset-failed e2e-s3 e2e-smtp 2>/dev/null || true
	systemd-run -q --unit=e2e-s3 /usr/local/bin/rclone serve s3 --config /dev/null --addr :443 \
		--cert /root/tls/s3.pem --key /root/tls/s3.key --auth-key "$access,$secret" /srv/s3
	systemd-run -q --unit=e2e-smtp python3 -I /root/smtp-sink.py /srv/mail 2525

	id sftpbackup >/dev/null 2>&1 || useradd --create-home --shell /bin/bash sftpbackup
	install -d -o sftpbackup -g sftpbackup -m 0700 /home/sftpbackup/.ssh
	install -o sftpbackup -g sftpbackup -m 0600 /dev/null /home/sftpbackup/.ssh/authorized_keys
	ssh-keygen -A >/dev/null
	systemctl enable --now ssh.socket >/dev/null 2>&1 || systemctl restart ssh
	for _ in $(seq 30); do
		if curl -sk -o /dev/null "https://127.0.0.1/" && (exec 3<>/dev/tcp/127.0.0.1/2525) 2>/dev/null; then
			exit 0
		fi
		sleep 1
	done
	echo "the S3 or SMTP sink didn't start: $(journalctl -u e2e-s3 -u e2e-smtp -n 20 --no-pager)" >&2
	exit 1
	;;
authorize)
	printf '%s\n' "$2" >>/home/sftpbackup/.ssh/authorized_keys
	;;
mail)
	for f in /srv/mail/*.eml; do
		[[ -e $f ]] || continue
		cat "$f"
		echo
	done
	;;
*)
	echo "usage: server.sh setup … | authorize <key> | mail" >&2
	exit 2
	;;
esac
