#!/usr/bin/env bash
# Protocol probes (design §14.4), sent as zimam, the panel's user: each
# malformed request gets its error code and leaves exactly one audit entry
# with that outcome.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

long=$(python3 -c 'print("{\"v\":1,\"pad\":\"" + "a" * (1 << 20) + "\"}")')
probe "a line over 1 MiB" invalid_request "$long"
probe "invalid JSON" invalid_request '{"v":1,'
probe "v 99" unsupported_protocol "{\"v\":99,\"id\":\"$ULID\",\"command\":\"agent.version\",\"actor\":\"probe\",\"params\":{}}"
probe "an unknown command" unknown_command "{$env_ok,\"command\":\"nope.nope\",\"params\":{}}"
probe "an unknown top-level field" invalid_request "{$env_ok,\"command\":\"agent.version\",\"params\":{},\"extra\":1}"
probe "an unknown params field" validation_failed "{$env_ok,\"command\":\"job.status\",\"params\":{\"job\":\"$ULID\",\"extra\":1}}"
probe "an uppercase key" invalid_request "{$env_ok,\"Command\":\"agent.version\",\"params\":{}}"
probe "a duplicate key" invalid_request "{$env_ok,\"command\":\"agent.version\",\"command\":\"agent.version\",\"params\":{}}"
exit "$failed"
