#!/usr/bin/env python3
"""Validate the portable production Compose bundle's security shape."""

from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
COMPOSE_FILE = Path("deploy/compose.yaml")
PRIVATE_CA_COMPOSE_FILE = Path("deploy/compose.private-ca.yaml")
RUNTIME_ENV_EXAMPLE = Path("deploy/velociportal.env.example")
STACK_ENV_FILE = Path("deploy/stack.env.example")
VERIFY_IMAGE = "ghcr.io/cybersader/velociportal:v0.0.0-verify"
VERIFY_CA_FILE = ROOT / ".env.example"


def fail(message: str) -> None:
    raise AssertionError(message)


def require(condition: bool, message: str) -> None:
    if not condition:
        fail(message)


def compose_json(arguments: list[str], environment: dict[str, str] | None = None) -> dict:
    command = ["docker", "compose", *arguments, "--format", "json"]
    try:
        result = subprocess.run(
            command,
            cwd=ROOT,
            env=environment,
            check=True,
            capture_output=True,
            text=True,
        )
    except FileNotFoundError as error:
        raise RuntimeError("docker is required to verify the production Compose bundle") from error
    except subprocess.CalledProcessError as error:
        if error.stderr:
            sys.stderr.write(error.stderr)
        raise RuntimeError("docker compose could not render the production bundle") from error

    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError("docker compose returned invalid JSON") from error


def only_service(model: dict) -> dict:
    services = model.get("services")
    require(isinstance(services, dict), "Compose model must contain a services object")
    require(list(services) == ["velociportal"], "production bundle must contain exactly one velociportal service")
    service = services["velociportal"]
    require(isinstance(service, dict), "velociportal service must be an object")
    require("build" not in service, "production bundle must not build source")
    require("container_name" not in service, "production bundle must not pin a container name")
    return service


def validate_service_networks(service: dict, *, require_explicit_upstreams_priority: bool) -> None:
    attachments = service.get("networks")
    require(isinstance(attachments, dict), "service network attachments must be explicit")
    require(
        len(attachments) == 2 and set(attachments) == {"default", "upstreams"},
        "service must attach only to default ingress and upstreams networks",
    )
    require(
        attachments["default"].get("gw_priority") == 1,
        "default ingress network must remain the preferred gateway",
    )
    if require_explicit_upstreams_priority:
        require(
            attachments["upstreams"].get("gw_priority") == 0 and "gw_priority" in attachments["upstreams"],
            "upstreams network gateway priority must remain explicit",
        )
    else:
        require(
            attachments["upstreams"].get("gw_priority", 0) == 0,
            "rendered upstreams network gateway priority changed unexpectedly",
        )


def validate_raw_networks(model: dict) -> None:
    networks = model.get("networks")
    require(isinstance(networks, dict), "Compose model must contain a networks object")
    require(
        len(networks) == 2 and set(networks) == {"default", "upstreams"},
        "production bundle must define exactly two networks",
    )

    ipam_config = networks["default"].get("ipam", {}).get("config")
    require(isinstance(ipam_config, list) and len(ipam_config) == 1, "default network must have one fixed IPAM range")
    require(
        ipam_config[0]
        == {
            "gateway": "${VELOCIPORTAL_GATEWAY:-172.31.255.1}",
            "subnet": "${VELOCIPORTAL_SUBNET:-172.31.255.0/24}",
        },
        "default network subnet and gateway changed unexpectedly",
    )

    upstreams = networks["upstreams"]
    require(upstreams.get("name") == "velociportal-upstreams", "upstreams network must have a stable Docker name")
    require(
        upstreams.get("internal") in (None, False),
        "upstreams network must preserve Headscale and NPM egress",
    )


def validate_rendered_networks(model: dict) -> None:
    networks = model.get("networks")
    require(isinstance(networks, dict), "rendered Compose model must contain a networks object")
    require(
        len(networks) == 2 and set(networks) == {"default", "upstreams"},
        "rendered bundle must define exactly two networks",
    )

    ipam_config = networks["default"].get("ipam", {}).get("config")
    require(
        ipam_config == [{"subnet": "172.31.255.0/24", "gateway": "172.31.255.1"}],
        "rendered default network must use the documented fixed subnet and gateway",
    )

    upstreams = networks["upstreams"]
    require(upstreams.get("name") == "velociportal-upstreams", "rendered upstreams network name changed unexpectedly")
    require(
        upstreams.get("internal") in (None, False),
        "rendered upstreams network must preserve Headscale and NPM egress",
    )


def validate_raw_ca_mount(service: dict) -> None:
    volumes = service.get("volumes")
    require(isinstance(volumes, list) and len(volumes) == 1, "private-CA overlay must add exactly one mount")
    ca_mount = volumes[0]
    require(ca_mount.get("type") == "bind", "private root must use a bind mount")
    require(
        ca_mount.get("source")
        == "${VELOCIPORTAL_CA_FILE:?set VELOCIPORTAL_CA_FILE to a readable public CA certificate}",
        "private root source must remain an explicitly required overlay value",
    )
    require(
        ca_mount.get("target") == "/etc/ssl/certs/velociportal-private-ca.crt",
        "private root target changed unexpectedly",
    )
    require(ca_mount.get("read_only") is True, "private root mount must be read-only")
    require(
        ca_mount.get("bind", {}).get("create_host_path") is False,
        "missing private-root source must fail instead of creating a directory",
    )


def validate_rendered_ca_mount(service: dict) -> None:
    volumes = service.get("volumes")
    require(isinstance(volumes, list) and len(volumes) == 1, "rendered private-CA overlay must add exactly one mount")
    ca_mount = volumes[0]
    require(ca_mount.get("type") == "bind", "rendered private root must use a bind mount")
    require(Path(str(ca_mount.get("source", ""))) == VERIFY_CA_FILE, "rendered private-root source changed unexpectedly")
    require(
        ca_mount.get("target") == "/etc/ssl/certs/velociportal-private-ca.crt",
        "rendered private root target changed unexpectedly",
    )
    require(ca_mount.get("read_only") is True, "rendered private-root mount must be read-only")
    require(
        ca_mount.get("bind", {}).get("create_host_path") in (None, False),
        "rendered private-root bind must not enable missing source creation",
    )


def validate_raw_model(model: dict, *, expect_private_ca: bool) -> None:
    require(model.get("name") == "velociportal-production", "production project name must not collide with repository Compose")
    service = only_service(model)

    require(
        service.get("image") == "${VELOCIPORTAL_IMAGE:?set VELOCIPORTAL_IMAGE to an immutable published tag or digest}",
        "image must remain an explicitly supplied immutable reference",
    )
    require(service.get("pull_policy") == "always", "service must always consult the registry for the selected image")
    require(service.get("user") == "65534:65534", "service must run as UID/GID 65534")
    require(service.get("read_only") is True, "service root filesystem must be read-only")
    require(service.get("cap_drop") == ["ALL"], "service must drop all Linux capabilities")
    require(
        service.get("security_opt") == ["no-new-privileges:true"],
        "service must enable no-new-privileges",
    )
    raw_ports = service.get("ports")
    require(
        raw_ports == ["127.0.0.1:18080:8080"]
        or raw_ports
        == [
            {
                "host_ip": "127.0.0.1",
                "mode": "ingress",
                "protocol": "tcp",
                "published": "18080",
                "target": 8080,
            }
        ],
        "raw application port must remain 127.0.0.1:18080:8080",
    )
    require("extra_hosts" not in service, "production service must not depend on host gateway aliases")
    validate_service_networks(service, require_explicit_upstreams_priority=True)

    env_files = service.get("env_file")
    require(isinstance(env_files, list) and len(env_files) == 1, "service must use exactly one application env file")
    require(env_files[0].get("format") == "raw", "application env file must use Compose raw format")
    require(env_files[0].get("required") is True, "application env file must remain required")

    environment = service.get("environment", {})
    require(
        environment.get("VELOCIPORTAL_ENV_FILE_ENCODING") == "go-quoted-v1",
        "runtime must enable go-quoted-v1 env decoding",
    )
    require(environment.get("LISTEN_ADDR") == "0.0.0.0:8080", "container listener must remain explicit")
    require(
        environment.get("TRUSTED_PROXY_CIDR")
        == "${VELOCIPORTAL_TRUSTED_PROXY_CIDR:?set the exact trusted proxy CIDR, normally the configured bridge gateway /32}",
        "trusted proxy CIDR must remain an explicit deployment value",
    )

    if expect_private_ca:
        validate_raw_ca_mount(service)
    else:
        require("volumes" not in service, "base production service must not mount host paths")

    healthcheck = service.get("healthcheck", {})
    require(
        healthcheck.get("test") == ["CMD", "/velociportal", "healthcheck"],
        "healthcheck must invoke the static binary directly",
    )
    validate_raw_networks(model)


def validate_rendered_model(model: dict, *, expect_private_ca: bool) -> None:
    require(model.get("name") == "velociportal-production", "rendered production project name changed unexpectedly")
    service = only_service(model)
    require(service.get("image") == VERIFY_IMAGE, "rendered image reference does not match the verification input")
    require(service.get("pull_policy") == "always", "rendered service must always pull the selected image")
    require(service.get("user") == "65534:65534", "rendered service must run as UID/GID 65534")
    require(service.get("read_only") is True, "rendered service root filesystem must be read-only")
    require(service.get("cap_drop") == ["ALL"], "rendered service must drop all Linux capabilities")
    require(
        service.get("security_opt") == ["no-new-privileges:true"],
        "rendered service must enable no-new-privileges",
    )
    require("extra_hosts" not in service, "rendered service must not depend on host gateway aliases")
    validate_service_networks(service, require_explicit_upstreams_priority=False)

    ports = service.get("ports")
    require(isinstance(ports, list) and len(ports) == 1, "rendered service must expose exactly one port")
    require(
        ports[0].get("host_ip") == "127.0.0.1"
        and ports[0].get("published") == "18080"
        and ports[0].get("target") == 8080,
        "rendered application publication must remain 127.0.0.1:18080:8080",
    )

    environment = service.get("environment", {})
    require(
        environment.get("HEADSCALE_URL") == '"http://headscale.velociportal.internal:8080"',
        "raw env-file mode must preserve the quoted internal HEADSCALE_URL",
    )
    require(
        environment.get("NPM_URL") == '"http://npm.velociportal.internal:81"',
        "raw env-file mode must preserve the quoted internal NPM_URL",
    )
    require(
        environment.get("NPM_PASSWORD") == '"replace-with-the-dedicated-npm-password"',
        "raw env-file mode must preserve quoted credential values",
    )
    require(
        environment.get("TRUSTED_PROXY_CIDR") == "172.31.255.1/32",
        "rendered trusted source must equal the fixed bridge gateway /32",
    )

    if expect_private_ca:
        validate_rendered_ca_mount(service)
    else:
        require("volumes" not in service, "rendered base service must not mount host paths")

    healthcheck = service.get("healthcheck", {})
    require(
        healthcheck.get("test") == ["CMD", "/velociportal", "healthcheck"],
        "rendered healthcheck must invoke the static binary directly",
    )
    validate_rendered_networks(model)


def raw_config_arguments(*compose_files: Path) -> list[str]:
    arguments: list[str] = []
    for compose_file in compose_files:
        arguments.extend(["-f", str(compose_file)])
    arguments.extend(["config", "--no-interpolate", "--no-normalize", "--no-path-resolution"])
    return arguments


def rendered_config_arguments(*compose_files: Path) -> list[str]:
    arguments = ["--env-file", str(STACK_ENV_FILE)]
    for compose_file in compose_files:
        arguments.extend(["-f", str(compose_file)])
    arguments.append("config")
    return arguments


def verification_environment() -> dict[str, str]:
    environment = os.environ.copy()
    # Keep verification deterministic even when the caller manages other stacks
    # with ambient Compose or deployment overrides.
    environment.pop("COMPOSE_PROJECT_NAME", None)
    environment.pop("VELOCIPORTAL_CA_FILE", None)
    environment.update(
        {
            "VELOCIPORTAL_IMAGE": VERIFY_IMAGE,
            "VELOCIPORTAL_ENV_FILE": "./velociportal.env.example",
            "VELOCIPORTAL_SUBNET": "172.31.255.0/24",
            "VELOCIPORTAL_GATEWAY": "172.31.255.1",
            "VELOCIPORTAL_TRUSTED_PROXY_CIDR": "172.31.255.1/32",
        }
    )
    return environment


def short_include_model() -> dict:
    with tempfile.TemporaryDirectory(prefix="velociportal-include-") as temporary_directory:
        temporary_root = Path(temporary_directory)
        bundle_directory = temporary_root / "bundle"
        bundle_directory.mkdir()

        (bundle_directory / "compose.yaml").write_text(
            (ROOT / COMPOSE_FILE).read_text(encoding="utf-8"),
            encoding="utf-8",
        )
        (bundle_directory / "velociportal.env").write_text(
            (ROOT / RUNTIME_ENV_EXAMPLE).read_text(encoding="utf-8"),
            encoding="utf-8",
        )
        (bundle_directory / ".env").write_text(
            "\n".join(
                (
                    f"VELOCIPORTAL_IMAGE={VERIFY_IMAGE}",
                    "VELOCIPORTAL_ENV_FILE=./velociportal.env",
                    "VELOCIPORTAL_SUBNET=172.31.255.0/24",
                    "VELOCIPORTAL_GATEWAY=172.31.255.1",
                    "VELOCIPORTAL_TRUSTED_PROXY_CIDR=172.31.255.1/32",
                    "",
                )
            ),
            encoding="utf-8",
        )

        include_wrapper = temporary_root / "truenas-install.yaml"
        include_wrapper.write_text(
            "include:\n"
            f"  - {json.dumps(str(bundle_directory / 'compose.yaml'))}\n"
            "services: {}\n",
            encoding="utf-8",
        )

        include_environment = os.environ.copy()
        for name in (
            "COMPOSE_PROJECT_NAME",
            "VELOCIPORTAL_IMAGE",
            "VELOCIPORTAL_ENV_FILE",
            "VELOCIPORTAL_SUBNET",
            "VELOCIPORTAL_GATEWAY",
            "VELOCIPORTAL_TRUSTED_PROXY_CIDR",
        ):
            include_environment.pop(name, None)

        return compose_json(
            ["--project-name", "velociportal-production", "-f", str(include_wrapper), "config"],
            include_environment,
        )


def main() -> int:
    base_environment = verification_environment()

    raw_base = compose_json(raw_config_arguments(COMPOSE_FILE), base_environment)
    validate_raw_model(raw_base, expect_private_ca=False)

    rendered_base = compose_json(rendered_config_arguments(COMPOSE_FILE), base_environment)
    validate_rendered_model(rendered_base, expect_private_ca=False)

    included_base = short_include_model()
    validate_rendered_model(included_base, expect_private_ca=False)

    raw_private_ca = compose_json(
        raw_config_arguments(COMPOSE_FILE, PRIVATE_CA_COMPOSE_FILE),
        base_environment,
    )
    validate_raw_model(raw_private_ca, expect_private_ca=True)

    private_ca_environment = base_environment.copy()
    private_ca_environment["VELOCIPORTAL_CA_FILE"] = str(VERIFY_CA_FILE)
    rendered_private_ca = compose_json(
        rendered_config_arguments(COMPOSE_FILE, PRIVATE_CA_COMPOSE_FILE),
        private_ca_environment,
    )
    validate_rendered_model(rendered_private_ca, expect_private_ca=True)

    print("production Compose bundle verified")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (AssertionError, RuntimeError) as error:
        print(f"production Compose verification failed: {error}", file=sys.stderr)
        raise SystemExit(1)
