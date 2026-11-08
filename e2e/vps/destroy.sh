#!/usr/bin/env bash
# Deletes this run's server and SSH key (labelled zimam-vps=<E2E_VPS_TAG>).
# vps.yml runs it whatever happened before; sweep.sh catches what it misses.
# Needs HCLOUD_TOKEN.
set -Eeuo pipefail
: "${E2E_VPS_TAG:?}" "${HCLOUD_TOKEN:?}"
python3 -I "$(dirname "$0")/hcloud.py" destroy "$E2E_VPS_TAG"
