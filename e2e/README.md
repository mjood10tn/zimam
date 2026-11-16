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
| Done-when 2 and recovery, in Chromium | `scripts/browser.sh --project recovery` (`browser/`) | host |
| The contract suite against the real agent, as `zimam` | `scenarios/m1-contract.sh` | container |
| The root-only Go tests | `scenarios/m1-roottests.sh` | container |
| Done-when 3 to 7 in Chromium: install PHP 8.4, create `static`, `php84` and `php85`, issue their certificates, switch `php84` to 8.5 and back under a request loop, make 8.4 the default. The specs call `scenarios/m2-*.sh` for the checks from outside the browser | `scripts/browser.sh --project m2-default` | host |
| The Git host that plays GitHub (below): a second container, `$E2E_CONTAINER-git`, holding `zimam-e2e/laravel-app`; the server's `agent.json` gets `e2e_git_host` | `githost/githost.sh up` | host |
| v0.2.0 done-when 1 to 8 in Chromium: a Laravel site connects the repository (the webhook secret and the deploy URL shown once, kept through polls, replaced by Regenerate, gone after a reload), deploys with Composer and the Laravel preset, has its `.env` edited (a refused save keeps the text), deploys again under a request loop, keeps its release through a failing script, rolls back, and deploys on a signed webhook and on its deploy URL; its Laravel log shows the app's line. The spec calls `scenarios/v02-deploy.sh` for the checks from outside the browser | `scripts/browser.sh --project v02-deploy` | host |
| The caller (`nobody`, `s_php84`), protocol, validation, log and job-limit (`e2e.memhog` ends `oom-kill`) probes between two file snapshots, then the `site.destroy` symlink, deploy (`probes/deploy.sh`) and maintenance (planted links, a 1 KiB cap) probes | `probes/run-all.sh` | container |
| Done-when 11: the audit log, for every command so far | `scenarios/audit.sh` | host |
| Running the installer again repairs, after `zimam-agent version --at-least` (D25) | `scenarios/m1-repair.sh` | host |
| The main run's container goes; the upgrade gets its own work directory and container | `scripts/down.sh` | host |
| The upgrade: a fresh container, Pebble and challtestsrv, with `E2E_WORK` and `E2E_CONTAINER` set to `zimam-upgrade` | `scripts/up.sh` | host |
| The previous release, built by its own `build.sh` | `scripts/build-previous.sh` | host |
| This tree as the next release, signed with the previous release's key | `scripts/build.sh` with `E2E_VERSION` and `E2E_RELEASE_KEY` | host |
| Install the previous release, set it up in Chromium, provision `up-static` and `up-php`, then run this tree's installer: the upgrade's checks, then a refused downgrade | `scenarios/upgrade.sh` | host |
| Logs for a failed run, then tear down | `scripts/collect-logs.sh`, `scripts/down.sh` | host |

`up.sh` writes `$E2E_WORK/state.env` (the bridge and container addresses), and every later step reads it. `E2E_WORK` defaults to `/tmp/zimam-e2e`; `E2E_CONTAINER`, `E2E_IMAGE` and `E2E_VERSION` can be set too (`scripts/common.sh`). `serve-release.sh` serves `$E2E_WORK/dist` by default, or the release in `E2E_DIST`, and reads the version from its manifest.

## The upgrade

`scenarios/upgrade.sh` checks that a server installed from the previous release takes this tree's release when the installer runs again (design §4, D25). It runs in a fresh container, after the main scenarios. In `e2e.yml` that is the same job: the main run's container goes first, and the steps after it use `E2E_WORK=$RUNNER_TEMP/zimam-upgrade` and `E2E_CONTAINER=zimam-upgrade`. `vps.yml` runs it on a fresh amd64 server of its own, in the `upgrade` job.

`build-previous.sh` builds the previous release with that release's own `e2e/scripts/build.sh`, unchanged, since this tree's script may rely on files the old tree lacks.
- The previous release is the newest tag before `HEAD`, from `git describe --tags --abbrev=0 HEAD^`, so the checkout needs its tags (`fetch-depth: 0`).
- The tag is checked out with `git worktree add` into `$E2E_WORK/tree-previous`. The worktree is removed when the build ends, whatever the outcome.
- The old `build.sh` runs with `E2E_WORK=$E2E_WORK/release-previous`, where `build-previous.sh` copies `state.env` first. Its release, contract copy, binaries and throwaway key land there.
- `build-previous.sh` also writes `release-previous/next-version`, the version for this tree's build.

This tree's `build.sh` then builds the current release.
- `E2E_RELEASE_KEY` points it at `release-previous/keys/release-key`, so one key signs both releases, as it does every real release.
- v0.1.0's `build.sh` reads no key variable and always makes its own key. That is why the key passes from the old build to the new one, not the other way.

The installer refuses to move a server to an older release (D25), in SemVer order, where a pre-release sorts before its release. So the two builds carry explicit versions:
- the previous release is built as the tag's version (`v0.1.0` gives `0.1.0`);
- this tree is built with the patch number one higher and the pre-release `e2e` (`0.1.1-e2e`).

The settings:

| Variable | Read by | Default |
| --- | --- | --- |
| `E2E_PREVIOUS_TAG` | `build-previous.sh` | `git describe --tags --abbrev=0 HEAD^` |
| `E2E_PREVIOUS_VERSION` | `build-previous.sh`, passed to the old `build.sh` as `E2E_VERSION` | the tag without its `v` |
| `E2E_RELEASE_KEY` | `build.sh`: sign with this private key instead of a new one | unset: a new throwaway key |
| `E2E_VERSION` | `build.sh` | `0.0.0-e2e`; set it to `release-previous/next-version` for the upgrade |
| `E2E_DIST` | `serve-release.sh` | `$E2E_WORK/dist` |

The scenario:
1. Serves the previous release and installs it with `scripts/install.sh`.
2. Completes setup through the setup link with the browser's `setup` project.
3. Provisions `up-static` and `up-php` through the socket with `probeclient`. `up-php` runs on the default PHP. Both must answer before the upgrade.
4. Serves this tree's release and runs the installer again. Then:
   - `zimam-agent version` reports the new version;
   - bootstrap installed the new panel release;
   - both sites answer 200 with their placeholder;
   - `/admin/login` answers 200;
   - `zimam-agent maintenance` exits 0;
   - the audit log has the cli:root `bootstrap` entries of both installer runs.
5. Serves the previous release again and runs its installer. This is the `refuses a downgrade` case: the installer must exit non-zero with D25's message, the new agent must stay, and the audit log must not grow.

## The Git host

v0.2.0's deploys fetch from GitHub over SSH with each site's deploy key and GitHub's pinned host keys. The run can't push to GitHub, so `githost/githost.sh` starts a Git host that plays it:
- OpenSSH, and a user `git` whose shell is `git-shell`, holding `~git/zimam-e2e/laravel-app.git` (`githost/server.sh`). Its host key is made when it starts.
- In LXD it is a second container on `lxdbr0`, reached at its address, port 22. On a VPS it is a Docker container on the runner, and a reverse SSH tunnel makes the server's own `127.0.0.1:2222` reach it.
- `up` sets `e2e_git_host` (address, port, host key) in the server's `agent.json`. Only e2e builds accept the field; a release build refuses an `agent.json` that has it. With it, `site.repo` writes the Git host's key to the site's `known_hosts` under the name `github.com`, and git's SSH command gains `-o HostKeyAlias=github.com -o HostName=<address> -o Port=<port>`. The remote is still `git@github.com:<owner>/<name>.git`, and every other option is the release build's.
- `authorize <key>` lets a site's deploy key fetch (the scenario plays the owner adding it on GitHub), and `commit <marker>` pushes a new commit whose `release.txt` holds the marker the app prints.

`fixtures/laravel-app` is Laravel-shaped but needs nothing from Packagist: its `composer.json` has no dependencies, so `composer install` runs for real and writes the autoloader the app and `artisan` need, without depending on Packagist or GitHub's download limits from CI. Its `artisan` does what the Laravel preset's commands do to a release: `storage:link`, `migrate --force`, `config:cache` (from the shared `.env`), `route:cache` and `view:cache`. The app prints its release marker and `E2E_GREETING`, and `?log=<word>` writes a line to `storage/logs/laravel.log`.

## Running it locally

On an Ubuntu host with LXD, or one where `snap install lxd` works (a VM is fine), run the steps in the order above as root or as a user with sudo. `build.sh` needs Go, PHP 8.3+ with Composer, and npm. If the LXD host has none of them, run `up.sh` there first, copy its `state.env` to a machine that has them, run `build.sh` there with the same `E2E_WORK`, and copy `$E2E_WORK` back.

`scripts/as.sh <user> <command>` runs a command in the container as a user, and `scripts/down.sh` removes everything `up.sh` started.

## What it relies on

- Pebble v2.10.1 and `pebble-challtestsrv`, pinned by SHA-256 in `up.sh`. Pebble's API certificate comes from a CA made on each run; its issuing root changes on every start, so `browser.sh` fetches it each time.
- The container's `/etc/hosts` points the test names at itself and `pebble` at the host, and `challtestsrv` answers the same names for Pebble's HTTP-01 validation.
- Chromium resolves `*.zimam.test` to the container with `--host-resolver-rules`, and trusts Pebble's root through the NSS database.
