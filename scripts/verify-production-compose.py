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
SERVICE_METADATA_COMPOSE_FILE = Path("deploy/compose.service-metadata.yaml")
RUNTIME_ENV_EXAMPLES = {
    "headscale": Path("deploy/velociportal.env.example"),
    "tailscale": Path("deploy/velociportal.tailscale.env.example"),
}
STACK_ENV_FILE = Path("deploy/stack.env.example")
VERIFY_IMAGE = "ghcr.io/cybersader/velociportal:v0.0.0-verify"
VERIFY_CA_FILE = ROOT / ".env.example"
VERIFY_SERVICE_METADATA_FILE = ROOT / "deploy/service-metadata.example.json"
VERIFY_SERVICE_METADATA_GID = "950"


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


def service_environment(service: dict) -> dict[str, str]:
    environment = service.get("environment", {})
    if isinstance(environment, dict):
        return environment
    require(isinstance(environment, list), "service environment must be a mapping or list")

    normalized: dict[str, str] = {}
    for entry in environment:
        require(isinstance(entry, str) and "=" in entry, "environment list entries must contain equals signs")
        key, value = entry.split("=", 1)
        require(key != "" and key not in normalized, "environment list keys must be unique and nonempty")
        normalized[key] = value
    return normalized


def mount_by_target(service: dict, target: str) -> dict:
    volumes = service.get("volumes", [])
    require(isinstance(volumes, list), "service volumes must be a list")
    matches = [mount for mount in volumes if mount.get("target") == target]
    require(len(matches) == 1, f"expected exactly one mount at {target}")
    return matches[0]


def validate_bind_mount(mount: dict, *, source: str | Path, target: str, rendered: bool) -> None:
    require(mount.get("type") == "bind", f"{target} must use a bind mount")
    if rendered:
        require(Path(str(mount.get("source", ""))) == source, f"rendered source for {target} changed unexpectedly")
    else:
        require(mount.get("source") == source, f"raw source for {target} changed unexpectedly")
    require(mount.get("target") == target, f"mount target {target} changed unexpectedly")
    require(mount.get("read_only") is True, f"mount {target} must be read-only")
    allowed_create = (None, False) if rendered else (False,)
    require(
        mount.get("bind", {}).get("create_host_path") in allowed_create,
        f"mount {target} must not create a missing host path",
    )


def validate_optional_mounts(service: dict, *, expect_private_ca: bool, expect_service_metadata: bool, rendered: bool) -> None:
    expected_count = int(expect_private_ca) + int(expect_service_metadata)
    volumes = service.get("volumes", [])
    require(isinstance(volumes, list) and len(volumes) == expected_count, "optional overlays added unexpected mounts")

    if expect_private_ca:
        source = VERIFY_CA_FILE if rendered else "${VELOCIPORTAL_CA_FILE:?set VELOCIPORTAL_CA_FILE to a readable public CA certificate}"
        validate_bind_mount(
            mount_by_target(service, "/etc/ssl/certs/velociportal-private-ca.crt"),
            source=source,
            target="/etc/ssl/certs/velociportal-private-ca.crt",
            rendered=rendered,
        )
    if expect_service_metadata:
        source = VERIFY_SERVICE_METADATA_FILE if rendered else "${VELOCIPORTAL_SERVICE_METADATA_FILE:?set VELOCIPORTAL_SERVICE_METADATA_FILE to a readable service metadata JSON file}"
        validate_bind_mount(
            mount_by_target(service, "/velociportal-services.json"),
            source=source,
            target="/velociportal-services.json",
            rendered=rendered,
        )
        environment = service_environment(service)
        require(
            environment.get("SERVICE_METADATA_FILE") == "/velociportal-services.json",
            "service metadata overlay must set the fixed in-container path",
        )
        groups = service.get("group_add")
        expected_group = VERIFY_SERVICE_METADATA_GID if rendered else "${VELOCIPORTAL_SERVICE_METADATA_GID:?set VELOCIPORTAL_SERVICE_METADATA_GID to the numeric group that can read the metadata file}"
        require(isinstance(groups, list) and [str(group) for group in groups] == [expected_group], "service metadata supplemental group changed unexpectedly")
    else:
        require("SERVICE_METADATA_FILE" not in service_environment(service), "base/CA-only service must not enable metadata")
        require("group_add" not in service, "base/CA-only service must not add supplemental groups")


def validate_raw_model(model: dict, *, expect_private_ca: bool, expect_service_metadata: bool = False) -> None:
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

    environment = service_environment(service)
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

    validate_optional_mounts(
        service,
        expect_private_ca=expect_private_ca,
        expect_service_metadata=expect_service_metadata,
        rendered=False,
    )

    healthcheck = service.get("healthcheck", {})
    require(
        healthcheck.get("test") == ["CMD", "/velociportal", "healthcheck"],
        "healthcheck must invoke the static binary directly",
    )
    validate_raw_networks(model)


def validate_rendered_model(
    model: dict,
    *,
    provider: str,
    expect_private_ca: bool,
    expect_service_metadata: bool = False,
) -> None:
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

    environment = service_environment(service)
    require(
        environment.get("CONTROL_PLANE") == f'"{provider}"',
        "raw env-file mode must preserve the explicit control-plane selector",
    )
    require(
        environment.get("NPM_URL") == '"http://npm.velociportal.internal:81"',
        "raw env-file mode must preserve the quoted internal NPM_URL",
    )
    require(
        environment.get("NPM_PASSWORD") == '"replace-with-the-dedicated-npm-password"',
        "raw env-file mode must preserve quoted credential values",
    )
    if provider == "headscale":
        require(
            environment.get("HEADSCALE_URL") == '"http://headscale.velociportal.internal:8080"',
            "Headscale mode must preserve the quoted internal HEADSCALE_URL",
        )
        require("TAILSCALE_OAUTH_CLIENT_ID" not in environment, "Headscale example must not contain Tailscale OAuth credentials")
        require("TAILSCALE_OAUTH_CLIENT_SECRET" not in environment, "Headscale example must not contain Tailscale OAuth credentials")
    elif provider == "tailscale":
        require("HEADSCALE_URL" not in environment, "Tailscale example must not contain Headscale configuration")
        require("HEADSCALE_API_KEY" not in environment, "Tailscale example must not contain Headscale credentials")
        require(
            environment.get("TAILSCALE_OAUTH_CLIENT_ID") == '"replace-with-the-dedicated-oauth-client-id"',
            "Tailscale mode must preserve the OAuth client ID placeholder",
        )
        require(
            environment.get("TAILSCALE_OAUTH_CLIENT_SECRET") == '"replace-with-the-dedicated-oauth-client-secret"',
            "Tailscale mode must preserve the OAuth client secret placeholder",
        )
        for forbidden in ("TAILSCALE_API_URL", "TAILSCALE_API_KEY", "TAILSCALE_ACCESS_TOKEN", "TAILSCALE_TAILNET"):
            require(forbidden not in environment, f"Tailscale example must not define {forbidden}")
    else:
        fail(f"unknown verification provider {provider!r}")
    require(
        environment.get("TRUSTED_PROXY_CIDR") == "172.31.255.1/32",
        "rendered trusted source must equal the fixed bridge gateway /32",
    )

    validate_optional_mounts(
        service,
        expect_private_ca=expect_private_ca,
        expect_service_metadata=expect_service_metadata,
        rendered=True,
    )

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


def verification_environment(runtime_env_example: Path) -> dict[str, str]:
    environment = os.environ.copy()
    # Keep verification deterministic even when the caller manages other stacks
    # with ambient Compose or deployment overrides.
    environment.pop("COMPOSE_PROJECT_NAME", None)
    environment.pop("VELOCIPORTAL_CA_FILE", None)
    environment.pop("VELOCIPORTAL_SERVICE_METADATA_FILE", None)
    environment.pop("VELOCIPORTAL_SERVICE_METADATA_GID", None)
    environment.update(
        {
            "VELOCIPORTAL_IMAGE": VERIFY_IMAGE,
            "VELOCIPORTAL_ENV_FILE": f"./{runtime_env_example.name}",
            "VELOCIPORTAL_SUBNET": "172.31.255.0/24",
            "VELOCIPORTAL_GATEWAY": "172.31.255.1",
            "VELOCIPORTAL_TRUSTED_PROXY_CIDR": "172.31.255.1/32",
        }
    )
    return environment


def short_include_model(runtime_env_example: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="velociportal-include-") as temporary_directory:
        temporary_root = Path(temporary_directory)
        bundle_directory = temporary_root / "bundle"
        bundle_directory.mkdir()

        (bundle_directory / "compose.yaml").write_text(
            (ROOT / COMPOSE_FILE).read_text(encoding="utf-8"),
            encoding="utf-8",
        )
        (bundle_directory / "velociportal.env").write_text(
            (ROOT / runtime_env_example).read_text(encoding="utf-8"),
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
    for provider, runtime_env_example in RUNTIME_ENV_EXAMPLES.items():
        base_environment = verification_environment(runtime_env_example)

        raw_base = compose_json(raw_config_arguments(COMPOSE_FILE), base_environment)
        validate_raw_model(raw_base, expect_private_ca=False)

        rendered_base = compose_json(rendered_config_arguments(COMPOSE_FILE), base_environment)
        validate_rendered_model(rendered_base, provider=provider, expect_private_ca=False)

        included_base = short_include_model(runtime_env_example)
        validate_rendered_model(included_base, provider=provider, expect_private_ca=False)

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
        validate_rendered_model(
            rendered_private_ca,
            provider=provider,
            expect_private_ca=True,
        )

        raw_service_metadata = compose_json(
            raw_config_arguments(COMPOSE_FILE, SERVICE_METADATA_COMPOSE_FILE),
            base_environment,
        )
        validate_raw_model(
            raw_service_metadata,
            expect_private_ca=False,
            expect_service_metadata=True,
        )

        service_metadata_environment = base_environment.copy()
        service_metadata_environment.update(
            {
                "VELOCIPORTAL_SERVICE_METADATA_FILE": str(VERIFY_SERVICE_METADATA_FILE),
                "VELOCIPORTAL_SERVICE_METADATA_GID": VERIFY_SERVICE_METADATA_GID,
            }
        )
        rendered_service_metadata = compose_json(
            rendered_config_arguments(COMPOSE_FILE, SERVICE_METADATA_COMPOSE_FILE),
            service_metadata_environment,
        )
        validate_rendered_model(
            rendered_service_metadata,
            provider=provider,
            expect_private_ca=False,
            expect_service_metadata=True,
        )

        combined_environment = service_metadata_environment.copy()
        combined_environment["VELOCIPORTAL_CA_FILE"] = str(VERIFY_CA_FILE)
        raw_combined = compose_json(
            raw_config_arguments(COMPOSE_FILE, PRIVATE_CA_COMPOSE_FILE, SERVICE_METADATA_COMPOSE_FILE),
            base_environment,
        )
        validate_raw_model(
            raw_combined,
            expect_private_ca=True,
            expect_service_metadata=True,
        )
        rendered_combined = compose_json(
            rendered_config_arguments(COMPOSE_FILE, PRIVATE_CA_COMPOSE_FILE, SERVICE_METADATA_COMPOSE_FILE),
            combined_environment,
        )
        validate_rendered_model(
            rendered_combined,
            provider=provider,
            expect_private_ca=True,
            expect_service_metadata=True,
        )

    print("production Compose bundle verified for headscale and tailscale")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (AssertionError, RuntimeError) as error:
        print(f"production Compose verification failed: {error}", file=sys.stderr)
        raise SystemExit(1)
