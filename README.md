# Zimam

Zimam is a server control panel for a clean Ubuntu 26.04 LTS server. A root Go agent is the only privileged code. A Laravel 13 + Filament 5 panel runs as the `zimam` user and talks to the agent over a Unix socket.

References such as "design §5.6" are to the project's design document, which isn't kept in this repository.

## Layout

| Path | Contents |
| --- | --- |
| `agent/` | The Go agent (`module zimam/agent`). It uses only the standard library and `golang.org/x/sys`. |
| `contract/` | JSON Schemas generated from the agent's Go types by `go generate ./...` in `agent/`. CI fails when they drift. |
| `panel/` | The Laravel 13 + Filament 5 panel, on SQLite. |
| `.github/workflows/` | CI. `ci.yml` runs on every pull request and every push to `master`. |

Later tasks add `installer/`, `e2e/` and `release/` (design §3).

## Agent development

Agent work happens in WSL with an Ubuntu 26.04 distro (design §14.6). Unit tests run as an ordinary user:

```bash
cd agent && test -z "$(gofmt -l .)" && go vet ./... && go test ./...
```

Every file root reads is rendered from a template, and `agent/internal/templates/testdata/golden/` holds the expected output. After changing a template, rewrite the golden files and review the diff:

```bash
cd agent && go test ./internal/templates -update
```

The `configcheck` job tests each golden case inside an Ubuntu 26.04 container, with the tools that read it: `nginx -t`, `php-fpm<v> -t`, `apt-cache policy`, `logrotate -d` and `systemd-analyze verify`. To run it locally with Docker:

```bash
cd agent && docker build -t zimam-configcheck scripts/configcheck && bash scripts/configcheck/run.sh zimam-configcheck internal/templates/testdata/golden
```

## Panel development

Panel work happens on Windows under Herd, with the fake agent client (`ZIMAM_AGENT=fake`, design §14.6):

```bash
cd panel && composer install && cp .env.example .env && php artisan key:generate && php artisan migrate && npm ci && npm run build
```

Then `herd link zimam-panel`, run in `panel/`, serves it at `http://zimam-panel.test`. Every page loads the panel's Filament theme (`resources/css/filament/admin/theme.css`, the tokens from the design system) through Vite, so run `npm run build` again after changing it, or keep `npm run dev` running. The tests don't need the build:

```bash
cd panel && php artisan test
```

`composer.json` sets `config.platform.php` to 8.3, the lowest PHP the panel supports, so the lock file is resolved for it (D7). CI runs the tests on PHP 8.3 and 8.5. On Windows, `composer` runs through cmd, which drops a `^` typed on the command line, so write version constraints there with `~`.

## Merge gates

Every check in design §14 blocks a merge. Once the repository is on GitHub, the owner marks every CI job as a required status check under branch protection for `master` (Settings → Branches, or a ruleset).

| Job | Workflow | Added by |
| --- | --- | --- |
| `agent` | `ci.yml` | Task 4 |
| `contract` | `ci.yml` | Task 8 |
| `configcheck` | `ci.yml` | Task 12 |
| `panel` | `ci.yml` | Task 17 |
| `installer` | `ci.yml` | Task 23 |

GitHub only offers a job as a required check after it has run at least once, so add each one after its first run on GitHub.
