#!/usr/bin/env python3
"""Replace a known shell-based scanner probe without changing its image/settings."""
import argparse
import copy
import http.client
import json
import re
import socket
import time
from urllib.parse import quote


def repair_plan(inspected):
    name = inspected.get("Name", "").lstrip("/")
    if not re.fullmatch(r"ocrypt-scanner-[a-z0-9-]+", name):
        raise ValueError("Only an ocrypt scanner may be repaired")
    config = copy.deepcopy(inspected["Config"])
    if config.get("Healthcheck", {}).get("Test") != ["CMD-SHELL", "/app/service --check-ready"]:
        raise ValueError("Expected the known shell-based scanner readiness probe")
    config["Healthcheck"]["Test"] = ["CMD", "/app/service", "--check-ready"]
    config["Image"] = inspected["Image"]  # Pin the already-running image object.
    config["HostConfig"] = copy.deepcopy(inspected["HostConfig"])
    endpoints = {}
    for network, original in inspected["NetworkSettings"]["Networks"].items():
        if original.get("IPAMConfig"):
            raise ValueError("Static network assignment requires a separate reviewed repair")
        endpoints[network] = {key: copy.deepcopy(original[key]) for key in ("Aliases", "Links", "DriverOpts") if original.get(key) is not None}
    config["NetworkingConfig"] = {"EndpointsConfig": endpoints}
    return config


class UnixConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect("/var/run/docker.sock")


class Docker:
    def request(self, method, path, body=None):
        connection = UnixConnection("localhost", timeout=35)
        payload = None if body is None else json.dumps(body).encode()
        try:
            connection.request(method, path, body=payload, headers={"Content-Type": "application/json"})
            response = connection.getresponse()
            data = response.read()
            if response.status >= 400:
                # Daemon messages can reflect supplied configuration. Never print it.
                raise RuntimeError(f"Docker request failed: HTTP {response.status}")
            return json.loads(data) if data else None
        finally:
            connection.close()


def repair(docker, container, backup, apply=False, wait_seconds=180):
    if not re.fullmatch(r"ocrypt-scanner-[a-z0-9-]+", container) or not re.fullmatch(r"ocrypt-scanner-[a-z0-9-]+", backup) or container == backup:
        raise ValueError("Use distinct ocrypt scanner and backup names")
    original = docker.request("GET", "/containers/" + quote(container, safe="") + "/json")
    if not original["State"].get("Running"):
        raise ValueError("Expected a running scanner; preserve stopped containers")
    plan = repair_plan(original)
    result = {"container": container, "backup": backup, "image_id": original["Image"],
              "healthcheck": plan["Healthcheck"]["Test"], "status": "dry-run"}
    if not apply:
        return result
    # Preflight the name before stopping anything. HTTP 404 is the only accepted
    # absence; permissions/connectivity failures must not be treated as absence.
    try:
        docker.request("GET", "/containers/" + quote(backup, safe="") + "/json")
    except RuntimeError as error:
        if str(error) != "Docker request failed: HTTP 404":
            raise
    else:
        raise ValueError("Backup name exists; inspect it before another repair")
    old_id = original["Id"]
    new_id = None
    stopped = renamed = False
    try:
        docker.request("POST", "/containers/" + old_id + "/stop?t=25")
        stopped = True
        docker.request("POST", "/containers/" + old_id + "/rename?name=" + quote(backup, safe=""))
        renamed = True
        created = docker.request("POST", "/containers/create?name=" + quote(container, safe=""), plan)
        new_id = created["Id"]
        actual = docker.request("GET", "/containers/" + new_id + "/json")
        if actual["Image"] != original["Image"] or actual["Config"]["Env"] != original["Config"]["Env"]:
            raise RuntimeError("Image or environment changed; rollback required")
        docker.request("POST", "/containers/" + new_id + "/start")
        deadline = time.monotonic() + wait_seconds
        while time.monotonic() < deadline:
            current = docker.request("GET", "/containers/" + new_id + "/json")
            if not current["State"].get("Running"):
                raise RuntimeError("Replacement scanner exited")
            if current["State"].get("Health", {}).get("Status") == "healthy":
                result.update(status="healthy", previous_id=old_id, replacement_id=new_id)
                return result
            time.sleep(2)
        raise RuntimeError("Replacement scanner readiness was not established")
    except Exception:
        if new_id:
            docker.request("POST", "/containers/" + new_id + "/stop?t=25")
            docker.request("DELETE", "/containers/" + new_id)
        if renamed:
            docker.request("POST", "/containers/" + old_id + "/rename?name=" + quote(container, safe=""))
        if stopped:
            docker.request("POST", "/containers/" + old_id + "/start")
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True)
    parser.add_argument("--backup-name", required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        print(json.dumps(repair(Docker(), args.container, args.backup_name, args.apply), indent=2))
    except (ValueError, RuntimeError, OSError) as error:
        raise SystemExit(str(error)) from None


if __name__ == "__main__":
    main()
