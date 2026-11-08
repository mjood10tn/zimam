# The real-server run

`.github/workflows/vps.yml` runs every e2e scenario and probe from `e2e.yml`
against fresh Hetzner Cloud servers, one amd64 (`cx`) and one arm64
(`cax`), instead of an LXD container (design §14.3, D2). Each server is
installed from an e2e release with `curl | bash`, against Let's Encrypt
staging, with UFW on. `release.yml` calls it before publishing; you can also
start it by hand (Actions → vps → Run workflow).

## What a run does

1. `create.sh`
   - Makes a throwaway SSH key.
   - Creates the server through the API (`hcloud.py`), labelled `zimam-vps=<run>-<attempt>-<arch>`. The API picks the Ubuntu 26.04 image, the cheapest shared type with 2 cores and 4 GB, and a location that has it.
   - Waits for SSH and cloud-init, and stops apt's timers.
   - Fetches Let's Encrypt's staging roots and checks them against the SHA-256 pins in `vps.yml`.
   - Waits for sslip.io to answer the test names.
2. The same scripts as `e2e.yml` run with `E2E_TARGET=vps`:
   - `lxc-ssh` stands in for `lxc`, so `in_c` and `lxc file push` go over SSH, through one master connection.
   - The test names sit under `<ip-with-dashes>.sslip.io` (`E2E_DOMAIN`).
   - The release is served on the server's `127.0.0.1:8080`.
3. `ipv6.sh`
   - Checks the panel and the sites over IPv4 and real DNS from the runner.
   - Checks IPv6 on the server: Nginx on `[::]`, UFW's v6 rules, and `curl -6` to the server's own address.
4. `destroy.sh` deletes the server and the key, whatever happened before.

`.github/workflows/vps-sweep.yml` runs `sweep.sh` daily. It deletes servers and keys labelled `zimam-vps` that are older than 3 hours, which is what a run that died before `destroy.sh` leaves behind. To test it, start `vps.yml` with **keep_server**, then start `vps-sweep.yml` with a small `hours`.

## Secrets

`HCLOUD_TOKEN`: a Read & Write token for the Hetzner Cloud project. It reaches only the create and delete steps.

## What isn't covered

- **Outside IPv6.** GitHub's runners have no IPv6, so traffic from outside the server over IPv6 isn't checked. A v6 firewall rule that drops outside traffic would pass.
- **IPv6 validation.** sslip.io names have only an A record, so Let's Encrypt validates over IPv4.
- **A real domain** is one variable away: set `E2E_DOMAIN` to a zone whose records point at the server.
