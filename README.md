# Zimam

Zimam is a server control panel for a clean Ubuntu 26.04 LTS server. A root Go agent is the only privileged code. A Laravel 13 + Filament 5 panel runs as the `zimam` user and talks to the agent over a Unix socket.

- Design: [docs/superpowers/specs/2026-10-25-zimam-walking-skeleton-design.md](docs/superpowers/specs/2026-10-25-zimam-walking-skeleton-design.md)
- Implementation plan: [docs/superpowers/plans/2026-10-26-zimam-walking-skeleton.md](docs/superpowers/plans/2026-10-26-zimam-walking-skeleton.md)
- Spike results: [docs/superpowers/spikes/2026-10-zimam-spikes.md](docs/superpowers/spikes/2026-10-zimam-spikes.md)

## Layout

| Path | Contents |
| --- | --- |
| `agent/` | The Go agent (`module zimam/agent`). It uses only the standard library and `golang.org/x/sys`. |
| `docs/superpowers/` | Design, plan and spike results. |
| `.github/workflows/` | CI. `ci.yml` runs on every pull request and every push to `master`. |

Later tasks add `contract/`, `panel/`, `installer/`, `e2e/` and `release/` (design §3).

## Agent development

Agent work happens in WSL with an Ubuntu 26.04 distro (design §14.6). Unit tests run as an ordinary user:

```bash
cd agent && test -z "$(gofmt -l .)" && go vet ./... && go test ./...
```

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
