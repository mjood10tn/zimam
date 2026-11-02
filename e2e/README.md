# End-to-end runs

The end-to-end harness (design §14.2): a release built with the `e2e` tag and a throwaway key, installed by `install.sh` in a fresh Ubuntu 26.04 LXD container, with [Pebble](https://github.com/letsencrypt/pebble) as the ACME server. `.github/workflows/e2e.yml` runs it on every pull request and on every push to `master`.

## The steps, in order

| Step | Script | Where |
| --- | --- | --- |
| LXD, the container, Pebble and `pebble-challtestsrv` | `scripts/up.sh` | host |
| A throwaway key, the e2e release, the panel with its dev dependencies, `probeclient`, the `roottest` binaries | `scripts/build.sh` | host with Go, PHP and Composer, and npm |
| Serve the release on the bridge, port 8080 | `scripts/serve-release.sh` | host |
| `curl … install.sh \| bash` in the container | `scripts/install.sh` | container |
| Done-when 1: the installer's last line is the setup link | `scenarios/m1-install.sh` | host |
| Done-when 2 and recovery, in Chromium | `scripts/browser.sh` (`browser/`) | host |
| The contract suite against the real agent, as `zimam` | `scenarios/m1-contract.sh` | container |
| The root-only Go tests | `scenarios/m1-roottests.sh` | container |
| The caller and protocol probes between two file snapshots | `probes/run.sh` | container |
| Done-when 11: the audit log | `scenarios/m1-audit.sh` | host |
| Running the installer again repairs, after `zimam-agent version --at-least` (D25) | `scenarios/m1-repair.sh` | host |
| Logs for a failed run, then tear down | `scripts/collect-logs.sh`, `scripts/down.sh` | host |

`up.sh` writes `$E2E_WORK/state.env` (the bridge and container addresses), and every later step reads it. `E2E_WORK` defaults to `/tmp/zimam-e2e`; `E2E_CONTAINER`, `E2E_IMAGE` and `E2E_VERSION` can be set too (`scripts/common.sh`).

## Running it locally

On an Ubuntu host with LXD, or one where `snap install lxd` works (a VM is fine), run the steps in the order above as root or as a user with sudo. `build.sh` needs Go, PHP 8.3+ with Composer, and npm. If the LXD host has none of them, run `up.sh` there first, copy its `state.env` to a machine that has them, run `build.sh` there with the same `E2E_WORK`, and copy `$E2E_WORK` back.

`scripts/as.sh <user> <command>` runs a command in the container as a user, and `scripts/down.sh` removes everything `up.sh` started.

## What it relies on

- Pebble v2.10.1 and `pebble-challtestsrv`, pinned by SHA-256 in `up.sh`. Pebble's API certificate comes from a CA made on each run; its issuing root changes on every start, so `browser.sh` fetches it each time.
- The container's `/etc/hosts` points the test names at itself and `pebble` at the host, and `challtestsrv` answers the same names for Pebble's HTTP-01 validation.
- Chromium resolves `*.zimam.test` to the container with `--host-resolver-rules`, and trusts Pebble's root through the NSS database.
