<p align="center">
  <img src="brand/zimam-logo-horizontal.svg" alt="Zimam" width="360">
</p>

<p align="center">
  A self-hosted server control panel for Ubuntu 26.04 LTS.<br>
  Run PHP and static sites on your own VPS, with a panel that can't hand out root.
</p>

<p align="center">
  <a href="https://github.com/mjood10tn/zimam/releases/latest">Latest release</a> ·
  <a href="#install">Install</a> ·
  <a href="#features">Features</a> ·
  <a href="#security-model">Security model</a> ·
  <a href="#development">Development</a>
</p>

---

> [!WARNING]
> **Zimam is early software.** v0.1.0 is the first release. It proves the core end to end: install, sites, PHP versions, certificates, monitoring, security settings and logs. Many features a production panel needs are still missing (see [Not yet](#not-yet)). Releases are signed with a development key, and the agent has not yet been reviewed by an outside security reviewer. Try it on a fresh server you can throw away, not on one that matters.

## What is Zimam?

Zimam (زِمَام, Arabic for "reins") is for solo developers who run their own VPS and want something like Forge or Ploi without a hosted service in the middle. You install it on a clean Ubuntu 26.04 server with one command. You then manage the server from a web panel: PHP versions, sites, HTTPS certificates, security settings, monitoring and logs.

It has two parts:

- **The agent** (`zimam-agent`) is a small Go program that runs as root. It is the only code with root privileges. It accepts a fixed set of validated commands over a Unix socket and writes every change to an audit log.
- **The panel** is a Laravel + Filament web app that runs as the unprivileged `zimam` user. It can only ask the agent to do things. It can't run shell commands as root, and it has no sudo.

## Features

**Install and access**
- One-command install on a clean server, which prints a one-time setup link.
- Two-factor authentication is required for the admin account. Login is throttled, and you can limit the panel to an IP allowlist.
- Lost your password or your 2FA device? `sudo zimam-agent panel setup-link` over SSH issues a new link.

**PHP**
- Several PHP versions side by side (8.2 to 8.5) from the [sury](https://deb.sury.org) packages. You install and remove them from the panel.
- One server default, which sets the version for new sites and for `php` over SSH.
- Each site runs on its own PHP version. You can switch a site's version with no failed requests.

**Sites**
- Static sites and generic PHP sites, each with a primary domain.
- Each site runs as its own Linux user (`s_<name>`) with its own PHP-FPM pool, so one site can't read another's files.
- Let's Encrypt certificates over HTTP-01, with automatic renewal and an HTTP-to-HTTPS redirect.
- Per-site security settings:
  - force HTTPS;
  - HSTS;
  - security headers;
  - IP allow and deny lists;
  - password protection;
  - blocking risky PHP functions;
  - keeping PHP inside the site's folder.

  A broken config never goes live: Zimam tests every change and rolls it back if the test fails.

**Monitoring and checks**
- Live CPU, memory, swap, disk, load and network figures, and a sample every minute kept for 7 days.
- A security screen that checks the server and each site, including:
  - the firewall and SSH settings;
  - pending security updates and whether a reboot is required;
  - world-writable files and home folder permissions;
  - certificates and the HTTPS redirect;
  - that every site's config still matches its template.

**Logs**
- A log viewer that can follow logs live and search them. It covers each site's Nginx and PHP logs, the system journal, SSH, Nginx, PHP-FPM, Certbot, APT and the panel's own log.
- Logs over 200 MB are rotated and compressed. Old archives are deleted after a retention period (14 days by default) and a size cap per site (5 GiB by default).
- An audit log of every change, every call that carries a secret and every rejected call. Passwords are redacted.

**Releases**
- Signed releases: the installer checks the manifest's Ed25519 signature and every file's SHA-256 before it runs anything it downloaded.

## Requirements

- A **clean** Ubuntu 26.04 LTS server (amd64 or arm64), without Nginx, Apache, PHP, MySQL or MariaDB installed.
- At least 2 GB of RAM (4 GB recommended) and 15 GB free on `/`.
- Root access over SSH.
- A domain name for the panel (for example `panel.example.com`) whose DNS already points at the server. Ports 80 and 443 must be reachable for Let's Encrypt.

## Install

On the server, as root:

```bash
curl -fsSL https://github.com/mjood10tn/zimam/releases/latest/download/install.sh | sudo bash -s -- --panel-domain panel.example.com
```

The installer:
1. Checks the server.
2. Installs Nginx, Certbot and UFW.
3. Creates the `zimam` user.
4. Downloads the agent and checks its signature.
5. Opens the firewall for SSH, HTTP and HTTPS.
6. Sets up PHP, the panel and its HTTPS certificate.

Its last line is a one-time setup link:

```
https://panel.example.com/setup/<token>
```

Open the link within 60 minutes, then set your email and password. After your first login, the panel makes you set up 2FA.

### Options

| Flag | Meaning |
| --- | --- |
| `--panel-domain <fqdn>` | Required. The panel's domain; it must already point at this server. |
| `--email <address>` | Email for the Let's Encrypt account. |
| `--php <x.y>` | Default PHP version for sites and the `php` command. Defaults to the newest supported version (8.5). |
| `--version <x.y.z>` | Release to install. Defaults to the latest. |
| `--skip-firewall` | Configure UFW but don't enable it. |
| `--yes` | Never prompt. |

### Re-running and recovery

- The installer is safe to run again. Every step checks before it acts, so a second run repairs a broken or interrupted install.
- To get a new setup link, run `sudo zimam-agent panel setup-link`. If the admin already exists, the link resets their password and 2FA. It also prints the panel IP allowlist.
- If the allowlist has locked you out, add `--clear-ip-allowlist`.

## Security model

Zimam is built on the assumption that the web panel is the part most likely to be attacked, so the panel gets no power of its own.

- **One privileged process.** Only the agent runs as root, and its socket accepts only root and `zimam`. Every request is decoded strictly: unknown fields, duplicate keys, oversized input and invalid values are refused, and the refusal itself is audited.
- **No path from a site to root.** Root never opens files inside a site's home folder. Work there runs as the site's own user through a small helper that can't follow symlinks or `..` out of the home.
- **Safe config changes.** Every config file comes from a template, and each change is tested (`nginx -t`, `php-fpm -t`, and so on) before it goes live. If the test fails, the previous files come back.
- **Tested on every change.** Each commit runs the agent's golden-file and fuzz tests, then a full end-to-end install on Ubuntu 26.04. That run includes a suite of root-escalation probes, which must all be refused and must leave the system unchanged. Before each release the same suite runs on real amd64 and arm64 servers.

Found a security issue? Please report it privately through [GitHub's security advisories](https://github.com/mjood10tn/zimam/security/advisories/new), not in a public issue.

## Not yet

These are planned but not in v0.1.0:

- Git deploys and Laravel-aware sites, queue workers and cron.
- MySQL, Valkey and Node.
- Backups.
- Notifications (warnings show on screen only).
- Editing firewall and SSH settings, and fail2ban.
- One-click upgrades of Zimam itself.
- Cloudflare mode.
- Extra domains per site, www redirects, wildcard certificates and DNS-01.
- Teams (one admin account for now).

## How it fits together

```
 browser ──HTTPS──▶ Nginx ──▶ panel (Laravel + Filament, user zimam)
                                   │
                                   │ JSON over /run/zimam/agent.sock
                                   ▼
                     zimam-agent (Go, root) ──▶ Nginx, PHP-FPM, Certbot, UFW, apt
                                   │
                                   └──▶ site-user helper (runs as s_<name>)
```

| Path | Contents |
| --- | --- |
| `agent/` | The Go agent (`module zimam/agent`). It uses only the standard library and `golang.org/x/sys`. |
| `contract/` | JSON Schemas for the agent's commands, generated from its Go types. CI fails when they drift. |
| `panel/` | The Laravel 13 + Filament 5 panel, on SQLite. |
| `installer/` | `install.sh` and its tests. |
| `release/` | The release public key. |
| `e2e/` | The end-to-end harness: LXD and real-server runs, browser tests and the root-escalation probes. |
| `brand/` | Logo and icons. |
| `.github/workflows/` | CI, end-to-end, nightly fuzzing, real-server runs and releases. |

## Development

### Agent

Agent work happens on Linux; on Windows, use WSL with an Ubuntu 26.04 distro. Unit tests run as an ordinary user:

```bash
cd agent && test -z "$(gofmt -l .)" && go vet ./... && go test ./...
```

Every file root writes is rendered from a template, and `agent/internal/templates/testdata/golden/` holds the expected output. After changing a template, rewrite the golden files and review the diff:

```bash
cd agent && go test ./internal/templates -update
```

CI's `configcheck` job tests each golden case in an Ubuntu 26.04 container with the tools that read it: `nginx -t`, `php-fpm<v> -t`, `apt-cache policy`, `logrotate -d` and `systemd-analyze verify`. To run it locally with Docker:

```bash
cd agent && docker build -t zimam-configcheck scripts/configcheck && bash scripts/configcheck/run.sh zimam-configcheck internal/templates/testdata/golden
```

### Panel

The panel runs locally against a fake agent (`ZIMAM_AGENT=fake`), so you don't need a server:

```bash
cd panel && composer install && cp .env.example .env && php artisan key:generate && php artisan migrate && npm ci && npm run build
```

Serve it with your usual local PHP setup (for example `herd link zimam-panel`). Run `npm run build` again after changing the theme, or keep `npm run dev` running. The tests don't need the build:

```bash
cd panel && php artisan test
```

`composer.json` sets `config.platform.php` to 8.3, the lowest PHP the panel supports, and CI tests on PHP 8.3 and 8.5. On Windows, `composer` runs through cmd, which drops a `^` typed on the command line, so write version constraints there with `~`.

### End-to-end

`e2e/README.md` explains how to run the full install and the probes in an LXD container, or against a real server.

### Checks

Every pull request must pass the `agent`, `contract`, `configcheck`, `panel`, `installer` and `e2e` checks.

## License

The license hasn't been chosen yet. Until one is added, all rights are reserved.
