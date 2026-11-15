# The real-server run

`.github/workflows/vps.yml` runs every e2e scenario and probe from `e2e.yml`
against fresh Hetzner Cloud servers, one amd64 (`cx`) and one arm64
(`cax`), instead of an LXD container (design §14.3, D2). Each server is
installed from an e2e release with `curl | bash`, against Let's Encrypt
staging, with UFW on. `release.yml` calls it before publishing; you can also
start it by hand (Actions → vps → Run workflow).

## What a run does

Three jobs per architecture: `create` runs step 1, `vps` steps 2 and 3, and `delete` step 4.

1. `create.sh`
   - Makes a throwaway SSH key.
   - Creates the server through the API (`hcloud.py`), labelled `zimam-vps=<run>-<attempt>-<arch>`. The API picks the Ubuntu 26.04 image, the cheapest shared type with 2 cores and 4 GB, and a location that has it.
   - Waits for SSH and cloud-init, and stops apt's timers.
   - Fetches Let's Encrypt's staging roots and checks them against the SHA-256 pins in `vps.yml`.
   - Waits for sslip.io to answer the test names.
   - Leaves what the next job needs in `$E2E_WORK/access/`, uploaded as `vps-access-<arch>`.
2. `attach.sh` takes that artifact into the `vps` job: the key, the host key accepted in step 1, the staging roots, and this runner's address as the server sees it. Then the same scripts as `e2e.yml` run with `E2E_TARGET=vps`:
   - `lxc-ssh` stands in for `lxc`, so `in_c` and `lxc file push` go over SSH, through one master connection.
   - The test names sit under `<ip-with-dashes>.sslip.io` (`E2E_DOMAIN`).
   - The release is served on the server's `127.0.0.1:8080`.
3. `ipv6.sh`
   - Checks the panel and the sites over IPv4 and real DNS from the runner.
   - Checks IPv6 on the server: Nginx on `[::]`, UFW's v6 rules, and `curl -6` to the server's own address.
4. `destroy.sh` deletes the server and the key, whatever happened before, unless the run was started with **keep_server**.

`create` also makes a third server, amd64 only to bound the cost, named by its run, `upgrade-amd64`. The `upgrade` job attaches to it and runs the upgrade scenario on it (`e2e/README.md`, "The upgrade"). `delete` removes it like the others.

`.github/workflows/vps-sweep.yml` runs `sweep.sh` daily. It deletes servers and keys labelled `zimam-vps` that are older than 3 hours, which is what a run that died before `destroy.sh` leaves behind. To test it, start `vps.yml` with **keep_server**, then start `vps-sweep.yml` with a small `hours`.

## Secrets

`HCLOUD_TOKEN`: a Read & Write token for the Hetzner Cloud project. It reaches only the two jobs `create` and `delete`, neither of which runs package code: each checks the repository out fresh, sets nothing up, and runs only `create.sh` or `destroy.sh` with `hcloud.py`. The `vps` job, which runs Composer and npm code, never sees it. It gets only the artifact `vps-access-<arch>` (one day's retention): the server's address and the throwaway SSH key that opens this run's server alone.

## What isn't covered

- **Outside IPv6.** GitHub's runners have no IPv6, so traffic from outside the server over IPv6 isn't checked. A v6 firewall rule that drops outside traffic would pass.
- **IPv6 validation.** sslip.io names have only an A record, so Let's Encrypt validates over IPv4.
- **A real domain** is one variable away: set `E2E_DOMAIN` to a zone whose records point at the server.
