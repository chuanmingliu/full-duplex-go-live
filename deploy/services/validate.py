#!/usr/bin/env python3
"""Validate all owned Compose models without starting services or reading secrets."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent


def tool_environment(source=None):
    """Keep CLI discovery only; shell Compose/model variables cannot alter fixtures."""
    source = os.environ if source is None else source
    keep = ("PATH", "HOME", "DOCKER_CONFIG", "TMPDIR", "TEMP", "TMP")
    result = {key: source[key] for key in keep if key in source}
    result["LC_ALL"] = "C"
    return result


def model(path, env, profiles=True, environment=None):
    command = ["docker", "compose", "--env-file", str(env), "-f", str(path)]
    if profiles:
        command += ["--profile", "*"]
    command += ["config", "--format", "json"]
    result = subprocess.run(command, capture_output=True, text=True, check=True,
                            env=tool_environment(environment))
    return json.loads(result.stdout)


def main():
    with tempfile.TemporaryDirectory(prefix="golive-compose-check-") as tmp:
        env = Path(tmp) / "fixture.env"
        body = (ROOT / "box/core.env.example").read_text()
        body = body.replace("sha256:REPLACE_WITH_VERIFIED_IMAGE_ID", "sha256:" + "0" * 64)
        body += "\nSOURCE_COMMIT=fixture-only-not-for-deployment\n"
        telephony_env = ROOT / "box/telephony.env.example"
        if telephony_env.exists():
            body += telephony_env.read_text()
            body += (ROOT / "box/rustpbx.env.example").read_text()
        env.write_text(body)
        files = [ROOT / "box/core.compose.yml", ROOT / "mac/dev.compose.yml"]
        telephony = ROOT / "box/telephony.compose.yml"
        if telephony.exists():
            files.append(telephony)
            files.append(ROOT / "box/rustpbx.staged.compose.yml")
        polluted = dict(os.environ, COMPOSE_PROFILES="core,quotes,demo,edge,telephony,pbx-migration,dev",
                        COMPOSE_FILE="/not/a/compose/file", COMPOSE_PROJECT_NAME="polluted",
                        GOLIVE_IMAGE="polluted:wrong", SERVICE_UID="9999", SERVICE_GID="9999",
                        CONFIG_DIR="/wrong/config", PROVIDERS_ENV="/wrong/private.env",
                        BOX_LAN_IP="203.0.113.99", SOURCE_COMMIT="wrong-source",
                        CRM_QUOTE_API_BASE="http://wrong.invalid", FOCUS_TOKEN_FILE="/wrong/token")
        for path in files:
            config = model(path, env)
            services = config["services"]
            assert services, path
            inactive = model(path, env, False)
            assert not inactive.get("services"), "No service may activate by default"
            assert model(path, env, environment=polluted) == config, "Shell polluted fixture values"
            assert model(path, env, False, polluted) == inactive, "Shell activated a default profile"
            for name, service in services.items():
                assert service.get("profiles"), name
                assert service.get("restart") in ("no", "unless-stopped"), name
                assert service.get("logging", {}).get("options", {}).get("max-size"), name
                for mount in service.get("volumes", []):
                    if mount["type"] == "bind":
                        assert not mount.get("bind", {}).get("create_host_path", True), (name, mount)
                if service.get("network_mode") == "host":
                    assert not service.get("ports"), name
            if path.name == "core.compose.yml":
                assert set(services) == {"phone", "crm", "journal", "desk", "quotesvc", "demo", "edge"}
                for name, service in services.items():
                    assert service["network_mode"] == "host", name
                    assert service["read_only"], name
                    assert service.get("healthcheck", {}).get("test"), name
                    assert service["user"] == "1000:1000", name
                phone = services["phone"]
                assert phone["environment"]["GOLIVE_LAST_CALL_FILE"] == "/last-call/last-call.json"
                assert any(m["target"] == "/last-call" and not m.get("read_only", False)
                           for m in phone["volumes"])
                assert services["demo"]["environment"]["GOLIVE_REALTIME_STORE_CALLS"] == "false"
                assert services["crm"]["environment"]["CRM_QUOTE_API_BASE"] == ""
                assert phone["environment"]["GOLIVE_FOCUS_TOKEN_FILE"] == "/edge/api.token"
                assert any(m["target"] == "/edge" and m.get("read_only", False)
                           for m in phone["volumes"])
                archive = next(m for m in services["journal"]["volumes"] if m["target"] == "/archive")
                assert not archive.get("read_only", False)
            print(f"validated {path.relative_to(ROOT)}: {', '.join(services)}")
    print("Intentional shell pollution did not change fixtures or activate profiles.")
    print("No Docker daemon, private configuration, or live service was used.")


if __name__ == "__main__":
    main()
