#!/usr/bin/env bash
# Deletes every server and SSH key labelled zimam-vps older than
# ${1:-3} hours: what a run that died before destroy.sh left. vps-sweep.yml
# runs it daily. Needs HCLOUD_TOKEN.
set -Eeuo pipefail
: "${HCLOUD_TOKEN:?}"
python3 -I "$(dirname "$0")/hcloud.py" sweep "${1:-3}"
