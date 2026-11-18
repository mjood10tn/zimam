# Root-escalation probes

The probes for design §14.4 and done-when 12. `run-all.sh` runs every one
of them in one pass, as the e2e workflow's probe step, after the milestone
2 and 3 scenarios, v0.2.0's v02-deploy, v0.3.0's v03-databases and v0.4.0's v04-workers (it needs
the site users `s_php84` and `s_php85`, php84's log archive, the Git host,
the Laravel site `laravel` with the webhook secret the browser kept, and
MySQL and Valkey, which v03-databases installs: that changes `/etc`, so it
happens before the first snapshot, and the scenario compares a pair of its
own). It
takes one `fs-hash` snapshot before the first probe and one after the
last, and fails on any difference. The snapshots
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
| Validation: v0.2.0's six commands, every field | `site.repo`: names, repositories (`.git`, a leading hyphen, `..`, a space, a path, `$(id)`, a 40-character owner), branches (`-rf`, `a..b`, `refs/heads/../x`, `.hidden/x`, `x.lock`, `a//b`, `a@{1}`, a trailing `/` or `.`, `~`, `^`, `:`, 201 bytes) and a non-boolean `rotate_key`; `site.deploy`: commits (uppercase, 39 and 41 characters, `HEAD`), a script with a NUL and one of 64 KiB + 1, a non-boolean `composer`, triggers, and `keep_releases` 0, 21, -1 and `"5"`; `site.rollback`: releases (`../initial`, `initial`, `/`, uppercase, a short date); `site.releases` and `site.env.read`: names; `site.env.write`: a NUL, 64 KiB + 1, a number | `validation.sh` |
| Validation: database names, v0.3.0 | `db.create` and `db.drop`: a backtick, `` `; DROP DATABASE mysql; -- ``, quotes, `;`, `--`, spaces, a newline, `/`, `\`, `%`, `*`, `$(id)`, uppercase, no prefix, another site's prefix, a name of 68 characters or one whose underscores count over 64, the reserved schemas; bad site names; `db.list` with bad site names; a non-boolean `drop_databases` | `validation.sh` |
| Validation: passwords, v0.3.0 | `db.user` and `cache.user`: a password with `'`, `\`, a newline, a quote, a space, `;`, a backtick, `$`, `%`, `:`, `#`, `>`, a non-ASCII character, 23 and 65 characters, an empty one; the refusal names `password`, and neither the reply nor the audit log holds it | `validation.sh` |
| Symlinks: `logs/php-error.log -> /etc/shadow`, then `logs.read` | `s_php84` swaps its PHP error log for the link (and plants an archive-named one); the read answers an error, never shadow's content, and `logs.list` doesn't list the link | `log-symlink.sh` |
| Symlinks: a link in `site/` to `/`, then `site.destroy` | the throwaway site `probe-static` is provisioned, its user plants `site/escape -> /`, and it is destroyed: its home and user are gone, nothing else changed | `destroy-symlink.sh` |
| Schedules and uids: `site.destroy` | the throwaway site `sb` may not use `crontab` or `at` (`/etc/cron.allow`, `/etc/at.allow`), is let in for a moment to make a crontab (and an at job when `at` is installed), as before v0.4.0, and destroyed: no entry of its old uid is left in `/var/spool/cron`; provisioned again, it gets a different uid | `destroy-schedules.sh` |
| Log archives | `../../../../etc/shadow`, absolute paths and other malformed names answer `validation_failed`; a well-formed name not in the listing answers `not_found`; a listed archive reads | `archive-names.sh` |
| Maintenance | `s_php84` plants archive-named links to `/etc/passwd`, `/root` and `/home/s_php85` and a backdated archive; `logs.policy` sets a 1 KiB cap; `zimam-agent maintenance` deletes only regular archives in sites' own log folders, every one audited; the old policy is sent back | `maintenance.sh` |
| Databases: a site's MySQL user, over the socket and over TCP | as `s_dbp-a`: another site's database can't be used or read, nor one whose name its grant pattern would match if underscores were wildcards; `SHOW DATABASES` lists only its own; `SELECT ... INTO OUTFILE`, `LOAD DATA INFILE` and `LOAD DATA LOCAL INFILE` (client option on, `local_infile` off) fail and write and read nothing; the `mysql` schema, `CREATE USER`, `GRANT ... ON *.*`, `CREATE DATABASE` and `SET GLOBAL` are denied; a wrong or empty password and root over TCP are refused | `databases.sh` |
| Databases: a site's Valkey user | as `s_dbp-a`, in databases 0 and 1: another site's keys (`GET`, `SET`, `DEL`), `EVAL` on another site's key declared or not, `CONFIG`, `ACL`, `FLUSHALL`, `FLUSHDB`, `KEYS`, `FUNCTION`, `SCRIPT FLUSH`, `SCRIPT KILL`, `SCRIPT DEBUG`, `PUBLISH` to another site's channel, `SHUTDOWN`, `REPLICAOF` and `DEBUG` are refused; its own keys and its own scripts work; the default user and a wrong password are refused | `databases.sh` |
| Databases: reachable only on the loopback | ports 3306, 6379 and 33060 don't answer on the server's address (and its IPv6 address on a VPS), from this host and from the server, and listen on loopback only | `databases.sh` |
| Databases: destroying a site | `site.destroy` of a site that has databases answers `conflict`; with `drop_databases`, the site leaves no MySQL account, grant or database, no Valkey user (in Valkey or `users.acl`) and no key in any of the 16 Valkey databases, while the other site keeps all of them | `databases.sh` |
| Validation: v0.4.0 | `site.workers` and `site.cron`: a command with `\n`, `\r` or `\0`, an empty one, one of 4097 bytes; schedules `@reboot`, `@midnight`, `@annually`, a sixth field (the cron line's seventh), one naming root, a `\n`, a `\r`, a `%`, values out of range, a step of 0, four fields, extra spaces, an empty one; the reserved job name `scheduler`; worker ids, counts, memory, `max_time` 30, a bad connection and queue, a command on a `laravel-queue` worker, queues on a custom one, an unknown kind, a duplicate, no list; bad site and worker names; `node.install` of 22, `26; rm -rf /`, the number 26 and `../26` | `validation.sh` |
| Workers and cron | on the throwaway PHP site `probe-wk`: inside a custom worker, `/home` holds only its own home, `/home/s_php84` and `/home/zimam` don't exist, `/root/.ssh` can't be reached, writing `/etc` fails read-only, `NoNewPrivs` is 1, and `sudo` and `su` give no root; a planted `.zimam/workers/w.sh -> /root/…` makes the restarted unit fail as the site user, the file's content in no journal, and the next `site.workers` replaces the link with the user's own script; commands that look like cron lines stay in the site's scripts, and every line of every `/etc/cron.d/zimam-*` file names only its site's user | `workers.sh` |
| Job limits | `e2e.memhog` (e2e builds only) touches 512 MiB under `MemoryMax=64M`; the job ends `failed` with `oom-kill` | `oom.sh` |
| Deploys | on the throwaway Laravel site `probe-deploy`, deploying the Git host's repository: deploy scripts that write `/etc/zimam-probe` or read `/root` or `/etc/shadow` fail with "Permission denied" and leave the live release; a planted `shared/.env` -> a file of the user's outside the home (`/var/tmp/…`) makes `site.env.read` answer `conflict` without its content, and `site.env.write` replace the link with the user's own 0600 file, the file outside untouched; planted `releases/<id> -> /` and `-> /var/tmp/…` (the user's own folder) are removed by the next deploy's prune, never followed; a commit not on the branch and an unknown release are refused, and `site.releases` of an unknown site answers `not_found` without an audit entry; a GitHub push with a bad signature (404) and a signed one over 1 MiB (Nginx's 413) start no job; the site is destroyed | `deploy.sh` |
| Proof | `fs-hash.sh` before the first probe and after the last: every entry under `/etc`, `/usr/local/sbin`, `/root`, `/home` and `/var/spool/cron` with its type, mode, owner, link target and hash | `run-all.sh` with `../scripts/fs-hash.sh` |

Beyond §14.4, `journal-paging.sh` checks `logs.read`'s journal paging
(design §11.2) against the real `journalctl`: pages of 3 back and forward
by cursor line up with one read of 6.
