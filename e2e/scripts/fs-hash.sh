#!/usr/bin/env bash
# Runs in the container, as root. Prints every entry under /etc,
# /usr/local/sbin, /root and /home with its type, mode, owner and symlink
# target, then every regular file's SHA-256: the proof that the probes left
# the system unchanged (design §14.4). Only what the system changes by
# itself is left out: the panel's database and storage, and site homes'
# logs/.
set -Eeuo pipefail

prune=(
	-path '/home/zimam/panel/shared/database.sqlite*' -o
	-path /home/zimam/panel/shared/storage -o
	-path '/home/s_*/logs'
)
roots=(/etc /usr/local/sbin /root /home)
find "${roots[@]}" -xdev \( "${prune[@]}" \) -prune -o -printf '%p\t%y\t%m\t%u:%g\t%l\n' | LC_ALL=C sort
find "${roots[@]}" -xdev \( "${prune[@]}" \) -prune -o -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
