#!/usr/bin/env python3
"""The Hetzner Cloud API calls of the real-server run (e2e/vps/README.md).

    hcloud.py create <tag> <arch> <public key file>
        Picks the Ubuntu 26.04 image, the cheapest shared cx (amd64) or cax
        (arm64) type with at least 2 cores and 4 GB, and a location that has
        it, then uploads the key and creates the server, both labelled
        zimam-vps=<tag>. Prints shell assignments: SERVER_ID, IPV4, IPV6.
    hcloud.py destroy <tag>
        Deletes the servers and SSH keys labelled zimam-vps=<tag>.
    hcloud.py sweep <hours>
        Deletes every server and SSH key labelled zimam-vps that is older.

The token comes from HCLOUD_TOKEN. Only the standard library is used.
"""
import datetime
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.hetzner.cloud/v1"
LABEL = "zimam-vps"
# Tried in this order; Falkenstein and Nuremberg have had both cx and cax.
LOCATIONS = ["fsn1", "nbg1", "hel1"]
ARCH = {"amd64": ("x86", "cx"), "arm64": ("arm", "cax")}


class APIError(Exception):
    def __init__(self, status, code, message):
        super().__init__(f"{status} {code}: {message}")
        self.code = code


def call(method, path, body=None, params=None):
    url = API + path + ("?" + urllib.parse.urlencode(params) if params else "")
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Authorization": "Bearer " + os.environ["HCLOUD_TOKEN"],
        "Content-Type": "application/json",
    })
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as e:
        try:
            err = json.loads(e.read()).get("error") or {}
        except ValueError:
            err = {}
        raise APIError(e.code, err.get("code", "?"), err.get("message", e.reason)) from None
    return json.loads(raw) if raw else {}


def all_pages(path, key, params=None):
    out, page = [], 1
    while True:
        r = call("GET", path, params={**(params or {}), "page": page, "per_page": 50})
        out += r[key]
        if not (r.get("meta") or {}).get("pagination", {}).get("next_page"):
            return out
        page += 1


def log(*args):
    print(*args, file=sys.stderr, flush=True)


def wait_action(action, seconds=300):
    end = time.time() + seconds
    while action["status"] == "running":
        if time.time() > end:
            raise SystemExit(f"action {action['command']} still running after {seconds} s")
        time.sleep(3)
        action = call("GET", f"/actions/{action['id']}")["action"]
    if action["status"] != "success":
        raise SystemExit(f"action {action['command']} ended {action['status']}: {action.get('error')}")


def pick(arch):
    cpu_arch, prefix = ARCH[arch]
    images = [i for i in all_pages("/images", "images", {"type": "system", "architecture": cpu_arch})
              if i.get("os_flavor") == "ubuntu" and i.get("os_version") == "26.04" and not i.get("deprecated")]
    if not images:
        raise SystemExit(f"no Ubuntu 26.04 image for {cpu_arch}")
    image = images[0]["name"]
    types = {t["id"]: t for t in all_pages("/server_types", "server_types")
             if t["architecture"] == cpu_arch and t["name"].startswith(prefix) and t["cpu_type"] == "shared"
             and t["cores"] >= 2 and t["memory"] >= 4 and not t.get("deprecated")}
    available = {}
    for dc in all_pages("/datacenters", "datacenters"):
        available.setdefault(dc["location"]["name"], set()).update(dc["server_types"]["available"])
    choices = []
    for loc in LOCATIONS:
        for tid in available.get(loc, set()) & types.keys():
            t = types[tid]
            price = next((float(p["price_hourly"]["gross"]) for p in t["prices"] if p["location"] == loc), 1e9)
            choices.append((LOCATIONS.index(loc), price, t["name"], loc))
    if not choices:
        raise SystemExit(f"no {prefix} type with 2 cores and 4 GB is available in {LOCATIONS}")
    # The cheapest type in each location, locations in order.
    best = {}
    for order, price, name, loc in sorted(choices):
        best.setdefault(loc, (order, price, name))
    ordered = sorted((v[0], v[1], v[2], loc) for loc, v in best.items())
    log(f"image {image}; candidates: " + ", ".join(f"{n} in {l} (€{p:.4f}/h)" for _, p, n, l in ordered))
    return image, [(name, loc) for _, _, name, loc in ordered]


def create(tag, arch, key_file):
    image, candidates = pick(arch)
    labels = {LABEL: tag}
    key = call("POST", "/ssh_keys", {"name": f"zimam-vps-{tag}", "public_key": open(key_file).read().strip(),
                                    "labels": labels})["ssh_key"]
    last = None
    for server_type, location in candidates:
        try:
            r = call("POST", "/servers", {
                "name": f"zimam-vps-{tag}", "server_type": server_type, "image": image, "location": location,
                "ssh_keys": [key["id"]], "labels": labels, "start_after_create": True,
                "public_net": {"enable_ipv4": True, "enable_ipv6": True},
            })
        except APIError as e:
            if e.code in ("resource_unavailable", "placement_error", "server_location_disabled"):
                log(f"{server_type} in {location}: {e}; trying the next")
                last = e
                continue
            raise
        server = r["server"]
        log(f"created server {server['id']}: {server_type} in {location}, {image}")
        wait_action(r["action"])
        net = server["public_net"]
        ipv6 = net["ipv6"]["ip"].split("/")[0] + "1"  # the /64's first address, which Hetzner configures
        print(f"SERVER_ID={server['id']}\nSERVER_TYPE={server_type}\nLOCATION={location}\nIMAGE={image}\n"
              f"IPV4={net['ipv4']['ip']}\nIPV6={ipv6}")
        return
    raise SystemExit(f"no server could be created: {last}")


def delete_all(servers, keys):
    for s in servers:
        log(f"deleting server {s['id']} {s['name']} ({s['created']})")
        try:
            call("DELETE", f"/servers/{s['id']}")
        except APIError as e:
            if e.code != "not_found":
                raise
    for k in keys:
        log(f"deleting SSH key {k['id']} {k['name']} ({k['created']})")
        try:
            call("DELETE", f"/ssh_keys/{k['id']}")
        except APIError as e:
            if e.code != "not_found":
                raise


def destroy(tag):
    sel = {"label_selector": f"{LABEL}={tag}"}
    delete_all(all_pages("/servers", "servers", sel), all_pages("/ssh_keys", "ssh_keys", sel))


def sweep(hours):
    cutoff = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=float(hours))

    def old(x):
        return datetime.datetime.fromisoformat(x["created"].replace("Z", "+00:00")) < cutoff

    sel = {"label_selector": LABEL}
    servers = [s for s in all_pages("/servers", "servers", sel) if old(s)]
    keys = [k for k in all_pages("/ssh_keys", "ssh_keys", sel) if old(k)]
    log(f"{len(servers)} servers and {len(keys)} SSH keys labelled {LABEL} are older than {hours} h")
    delete_all(servers, keys)


if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    {"create": create, "destroy": destroy, "sweep": sweep}[cmd](*args)
