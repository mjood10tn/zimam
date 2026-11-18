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
| v0.3.0 in Chromium: two Laravel sites, `alpha` and `beta`; MySQL and Valkey installed from the panel; a database each (and a second one for `alpha`, dropped again); each site's MySQL user and Valkey user, shown once, written to `.env` (the site picks `CACHE_STORE` and `QUEUE_CONNECTION` itself); the fixture app deployed, writing its own row and cache key and running a queue job through Valkey; `alpha`'s passwords rotated, which closes its queue worker, and a redeploy; `beta` deleted, first without its databases (refused) and then with them, then `alpha`. The spec calls `scenarios/v03-databases.sh` for the checks from outside the browser, among them one before-and-after file snapshot: only what installing MySQL and Valkey changes may differ | `scripts/browser.sh --project v03-databases` | host |
| v0.4.0: Node.js 24, a change of major to 26, an uninstall and 26 again; the Laravel site `workers` deploys with `npm ci && npm run build` in its script; a `laravel-queue` worker with 2 processes on Valkey runs a queued job; the scheduler and a `*/1` cron job run (the scheduled command's file within two minutes, the job's line in `cron.log`); a deploy and a switch to PHP 8.5 restart both processes onto the new release and binary; deleting the site leaves no unit, target, script, cron file or process. Also cron runs and `/etc/cron.allow` and `/etc/at.allow` name only root | `scenarios/v04-workers.sh` | host |
| v0.4.0 in Chromium, on the site `laravel`: a custom worker added and shown running, a cron job added with its next runs, the scheduler on and off, the Node.js page's version; both deleted again. Screenshots of the two tabs and the page at 400 and 1280 px go to `$E2E_WORK/screens` | `scripts/browser.sh --project v04-workers` | host |
| The caller (`nobody`, `s_php84`), protocol, validation, log and job-limit (`e2e.memhog` ends `oom-kill`) probes between two file snapshots, then the `site.destroy` symlink, deploy (`probes/deploy.sh`), database and cache (`probes/databases.sh`), workers (`probes/workers.sh`), and maintenance (planted links, a 1 KiB cap) probes | `probes/run-all.sh` | container |
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
   - both sites' state files keep `template_version: 1` and have no `repository`, `releases` or `keep_releases` (R02-D9);
   - both vhosts, `up-php`'s FPM pool and both sites' logrotate rules have the same SHA-256 as before the upgrade;
   - the audit log has the cli:root `bootstrap` entries of both installer runs.
5. Serves the previous release again and runs its installer. This is the `refuses a downgrade` case: the installer must exit non-zero with D25's message, the new agent must stay, and the audit log must not grow.

## The Git host

v0.2.0's deploys fetch from GitHub over SSH with each site's deploy key and GitHub's pinned host keys. The run can't push to GitHub, so `githost/githost.sh` starts a Git host that plays it:
- OpenSSH, and a user `git` whose shell is `git-shell`, holding `~git/zimam-e2e/laravel-app.git` (`githost/server.sh`). Its host key is made when it starts.
- In LXD it is a second container on `lxdbr0`, reached at its address, port 22. On a VPS it is a Docker container on the runner, and a reverse SSH tunnel makes the server's own `127.0.0.1:2222` reach it.
- `up` sets `e2e_git_host` (address, port, host key) in the server's `agent.json`. Only e2e builds accept the field; a release build refuses an `agent.json` that has it. With it, `site.repo` writes the Git host's key to the site's `known_hosts` under the name `github.com`, and git's SSH command gains `-o HostKeyAlias=github.com -o HostName=<address> -o Port=<port>`. The remote is still `git@github.com:<owner>/<name>.git`, and every other option is the release build's.
- `authorize <key>` lets a site's deploy key fetch (the scenario plays the owner adding it on GitHub), and `commit <marker>` pushes a new commit whose `release.txt` holds the marker the app prints.

A failed spec leaves a Playwright trace (`retain-on-failure`) in the uploaded logs. A trace of `v02-deploy` holds what the panel revealed: the webhook secret, the deploy URL's token and the site's `.env`. They are throwaway values that work only on this run's server, which is gone when the run ends, except on a VPS kept with **keep_server** (`vps/README.md`).

`fixtures/laravel-app` is Laravel-shaped but needs nothing from Packagist: its `composer.json` has no dependencies, so `composer install` runs for real and writes the autoloader the app and `artisan` need, without depending on Packagist or GitHub's download limits from CI. Its `artisan` does what the Laravel preset's commands do to a release: `storage:link`, `migrate --force`, `config:cache` (from the shared `.env`), `route:cache` and `view:cache`. The app prints its release marker and `E2E_GREETING`, and `?log=<word>` writes a line to `storage/logs/laravel.log`. With the DB_ and REDIS_ keys in its `.env` (and `CACHE_STORE` and `QUEUE_CONNECTION` set to `redis`), `?db=<word>` writes a row to the site's MySQL database and reads it back, `?cache=<word>` does the same with a key in Valkey database 1 (where Laravel's Redis cache lives), `?queue=<word>` pushes a job to the Redis queue with Laravel's Lua scripts, and `?done=<word>` says whether `php artisan queue:work` ran it. Its `artisan` has `queue:work`, which ends with the error when Valkey closes its connection; each job it runs leaves `~/e2e/job-<word>` with the PHP version and the release that ran it. `schedule:run` adds a line of the same to `~/e2e/schedule.log`. `npm ci && npm run build` builds its front end in the shape Vite leaves (`public/build/manifest.json` and one asset, plus `node.txt` with the Node version), without Vite: `package.json` has no dependencies and `.npmrc` keeps npm offline, so the build needs nothing from the npm registry. No deploy script calls `cache:clear`: a site's Valkey user can't `FLUSHDB`.

## Running it locally

On an Ubuntu host with LXD, or one where `snap install lxd` works (a VM is fine), run the steps in the order above as root or as a user with sudo. `build.sh` needs Go, PHP 8.3+ with Composer, and npm. If the LXD host has none of them, run `up.sh` there first, copy its `state.env` to a machine that has them, run `build.sh` there with the same `E2E_WORK`, and copy `$E2E_WORK` back.

`scripts/as.sh <user> <command>` runs a command in the container as a user, and `scripts/down.sh` removes everything `up.sh` started.

A second run on a host that already runs one shares nothing with it when these are set (`scripts/common.sh`; the defaults are what CI uses): `E2E_WORK`, `E2E_CONTAINER`, `E2E_PORT` (the release server), `E2E_PEBBLE_PORT`, `E2E_PEBBLE_MGMT_PORT`, `E2E_DNS_PORT` and `E2E_CHALL_PORT` (Pebble and challtestsrv), `E2E_UNIT` (the host units are `$E2E_UNIT-pebble`, `-challtestsrv` and `-www`), `E2E_PROFILE` (the LXD profile `up.sh` writes and `down.sh` deletes, unless it is the default `zimam-e2e`) and `E2E_NSS_NICK` (the name Chromium's NSS database keeps the run's root under).

## What it relies on

- Pebble v2.10.1 and `pebble-challtestsrv`, pinned by SHA-256 in `up.sh`. Pebble's API certificate comes from a CA made on each run; its issuing root changes on every start, so `browser.sh` fetches it each time.
- The container's `/etc/hosts` points the test names at itself and `pebble` at the host, and `challtestsrv` answers the same names for Pebble's HTTP-01 validation.
- Chromium resolves `*.zimam.test` to the container with `--host-resolver-rules`, and trusts Pebble's root through the NSS database.
