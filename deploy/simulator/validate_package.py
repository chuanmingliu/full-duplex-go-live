#!/usr/bin/env python3
"""Validate this isolated Compose package; never starts/stops containers."""
import argparse
import json
from pathlib import Path
import re
import socket
import subprocess

PROJECT = "golive-user-simulator"
ROOT = Path(__file__).resolve().parent


def validate(config):
    errors = []
    if config.get("name") != PROJECT:
        errors.append("unexpected project name")
    services = config.get("services", {})
    if set(services) != {"config", "simulator", "activecall"}:
        errors.append("unexpected service set")
    for name, service in services.items():
        if not service.get("image", "").startswith(PROJECT + (":" if name != "activecall" else "-activecall:")):
            errors.append(f"{name}: unrelated image")
        if service.get("container_name") or service.get("network_mode") or service.get("privileged") or service.get("devices"):
            errors.append(f"{name}: shared host/container access")
        for mount in service.get("volumes", []):
            if mount.get("type") != "volume" or mount.get("source") not in {"gateway-config", "run-data"}:
                errors.append(f"{name}: only dedicated named volumes are allowed")
        for port in service.get("ports", []):
            if name != "simulator" or port.get("host_ip") != "127.0.0.1" or int(port.get("target", 0)) != 18865 or str(port.get("published")) != "18865":
                errors.append(f"{name}: external SIP/RTP or unapproved host port; dedicated network review required")
        if set(service.get("networks", {})) != {"simulator"}:
            errors.append(f"{name}: shared network")
    for category in ("networks", "volumes"):
        for resource in config.get(category, {}).values():
            if resource.get("external") or not resource.get("name", "").startswith(PROJECT + "_"):
                errors.append(f"{category}: external or unrelated resource")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--static-only", action="store_true", help="skip occupied-port/container checks")
    args = parser.parse_args()
    command = ["docker", "compose", "-p", PROJECT, "-f", str(ROOT / "compose.yml")]
    rendered = subprocess.run(command + ["config", "--format", "json"], check=True, capture_output=True, text=True)
    errors = validate(json.loads(rendered.stdout))  # contains secrets; never print it
    for dockerfile in (ROOT / "Dockerfile", ROOT / "activecall/Dockerfile"):
        stages = {"scratch"}
        for line in dockerfile.read_text().splitlines():
            if not line.startswith("FROM "):
                continue
            parts=line.split()
            if parts[1] not in stages and not re.search(r"@sha256:[0-9a-f]{64}(?: |$)", line):
                errors.append(f"{dockerfile.name}: dependency image lacks immutable digest")
            if len(parts)>=4 and parts[2].upper()=="AS":
                stages.add(parts[3])
    if not args.static_only:
        with socket.socket() as sock:
            try:
                sock.bind(("127.0.0.1", 18865))
            except OSError:
                errors.append("controller port18865 is occupied")
        running = subprocess.run(["docker", "ps", "-q", "--filter", "label=com.docker.compose.project=" + PROJECT], check=True, capture_output=True, text=True)
        if running.stdout.strip():
            errors.append("this simulator project already has running containers")
    if errors:
        raise SystemExit("Package rejected:\n" + "\n".join(errors))
    print("Isolated local package valid. SIP/RTP stays inside its dedicated Docker network.")


if __name__ == "__main__":
    main()
