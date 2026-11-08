# Root-escalation probes

The probes for design §14.4 and done-when 12. `run-all.sh` runs every one
of them in one pass, as the e2e workflow's probe step, after the milestone
2 and 3 scenarios (it needs the site users `s_php84` and `s_php85` and
php84's log archive). It takes one `fs-hash` snapshot before the first
probe and one after the last, and fails on any difference. The snapshots
leave out only what `e2e/scripts/fs-hash.sh`'s prune list names, the one
place the exclusions live. `lib.sh` holds what the probes share: sending
raw lines as `zimam` with `probeclient`, submitting jobs and reading the
audit log's new entries.

Every rejection must also leave exactly one `error:<code>` audit entry;
`e2e/scenarios/audit.sh` (done-when 11) re-checks the whole audit log
afterwards.

| §14.4 item | What is sent | Script |
| --- | --- | --- |
| Callers | `nobody` and the site user `s_php84` connect, each also given the socket's group to reach the agent's own check | `callers.sh` |
| Protocol | a line over 1 MiB, invalid JSON, `v` 99, an unknown command, an unknown top-level field, an unknown params field, an uppercase key, a duplicate key | `protocol.sh` |
| Validation: site names | dots, slashes, `..`, uppercase, Unicode, 25 characters, the reserved names (`site.provision`; `site.destroy` and `cert.issue` with traversal and uppercase) | `validation.sh` |
| Validation: domains | a space, `;`, `{`, a newline, `$`, both quotes, a wildcard | `validation.sh` |
| Validation: PHP versions | `8.5; rm -rf /`, `../8.4`, `8.1` (not on the allowlist), to `php.install`, `site.provision` and `site.php` | `validation.sh` |
| Validation: malformed CIDRs | prefixes past 32 and 128, host bits set, a bad address, a hostname, a port, a command after the address, a negative prefix, a space, in `ip_allow` and `ip_deny` | `validation.sh` |
| Validation: non-bcrypt hashes | plain text, MD5, the `$2a$` and `$2b$` prefixes, costs 4 and 15, a hash one character short | `validation.sh` |
| Validation: usernames | `:`, a newline, a carriage return, an empty name | `validation.sh` |
| Symlinks: `logs/php-error.log -> /etc/shadow`, then `logs.read` | `s_php84` swaps its PHP error log for the link (and plants an archive-named one); the read answers an error, never shadow's content, and `logs.list` doesn't list the link | `log-symlink.sh` |
| Symlinks: a link in `site/` to `/`, then `site.destroy` | the throwaway site `probe-static` is provisioned, its user plants `site/escape -> /`, and it is destroyed: its home and user are gone, nothing else changed | `destroy-symlink.sh` |
| Log archives | `../../../../etc/shadow`, absolute paths and other malformed names answer `validation_failed`; a well-formed name not in the listing answers `not_found`; a listed archive reads | `archive-names.sh` |
| Maintenance | `s_php84` plants archive-named links to `/etc/passwd`, `/root` and `/home/s_php85` and a backdated archive; `logs.policy` sets a 1 KiB cap; `zimam-agent maintenance` deletes only regular archives in sites' own log folders, every one audited; the old policy is sent back | `maintenance.sh` |
| Job limits | `e2e.memhog` (e2e builds only) touches 512 MiB under `MemoryMax=64M`; the job ends `failed` with `oom-kill` | `oom.sh` |
| Proof | `fs-hash.sh` before the first probe and after the last: every entry under `/etc`, `/usr/local/sbin`, `/root` and `/home` with its type, mode, owner, link target and hash | `run-all.sh` with `../scripts/fs-hash.sh` |

Beyond §14.4, `journal-paging.sh` checks `logs.read`'s journal paging
(design §11.2) against the real `journalctl`: pages of 3 back and forward
by cursor line up with one read of 6.
