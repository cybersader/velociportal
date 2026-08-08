# CLI and Make targets

Velociportal is one static binary. The server remains environment-configured, while setup, diagnostics, and health probing are explicit commands that can run without starting the server.

## Binary commands

```text
velociportal
velociportal serve [--env-file FILE] [--listen ADDR]
velociportal setup [--env-file FILE]
velociportal setup observe-proxy [options]
velociportal doctor [options]
velociportal validate [options]
velociportal healthcheck [options]
velociportal help [command]
```

| Command | Behavior |
|---|---|
| `velociportal` | Starts the server from the process environment |
| `velociportal serve` | Starts the server from the process environment |
| `velociportal serve --env-file FILE` | Loads configuration only from `FILE`, then starts the server |
| `velociportal serve --env-file FILE --listen ADDR` | Uses the literal file values but overrides only the listener, which Compose uses for the bridged container |
| `velociportal setup --env-file FILE` | Runs the guided wizard and atomically creates or updates the local environment file |
| `velociportal setup observe-proxy` | Observes the immediate peer on a one-time URL and proposes an exact trusted source for confirmation |
| `velociportal doctor` | Validates configuration, upstreams, snapshot construction, and join coverage |
| `velociportal doctor --identity LOGIN` | Also previews the cards produced for `LOGIN`; the option may be repeated |
| `velociportal validate --identity LABEL=LOGIN ...` | Compares at least two labeled identities and emits explainable text or JSON join evidence |
| `velociportal healthcheck` | Probes `/healthz` without loading application configuration or credentials |
| `velociportal help COMMAND` | Prints command-specific usage |

Command exit codes are stable:

- **`0`** — success; doctor warnings do not make the command fail, while validate requires no unresolved review findings.
- **`1`** — operational failure or a complete validation report that still requires operator review.
- **`2`** — command-line usage error.

!!! note "Environment-file behavior"
    Environment files are parsed as configuration data. Velociportal does not execute shell interpolation, variable expansion, or command substitution, and file-backed commands do not merge missing values from the process environment. Compose 2.30+ loads the file in raw mode, then the binary applies the same strict unquoted, single-quoted, or Go-style double-quoted value grammar used by file-backed commands. Credentials containing `$`, `${...}`, quotes, spaces, `#`, or backslashes therefore reach runtime unchanged, while malformed quoting fails startup instead of becoming a different credential.

## Setup

```bash
velociportal setup --env-file .env
```

The wizard:

- prompts for Headscale and NPM connection details;
- reads API keys and passwords through hidden terminal input;
- preserves existing hidden secrets when Enter is pressed;
- validates values before writing;
- preserves unknown environment-file keys;
- locks the environment-file directory against another cooperating setup/observer command;
- writes atomically with owner-only mode `0600`; and
- never guesses `TRUSTED_PROXY_CIDR`.

The lock closes the check/replace race between Velociportal commands. Unrelated editors do not honor this advisory lock, so avoid manually editing the file while setup or observation is running.

Secret values are not accepted through command-line flags and are never printed.

### Observe the trusted proxy source

```bash
velociportal setup observe-proxy \
  --env-file .env \
  --listen 127.0.0.1:8080 \
  --timeout 2m
```

The observer generates a cryptographically random one-time path to append to the existing browser-facing proxy origin, records only the connection's immediate `RemoteAddr`, ignores forwarding and identity headers, and proposes an exact IPv4 `/32` or IPv6 `/128`. After explicit confirmation it changes only the `TRUSTED_PROXY_CIDR` value, while canonically rewriting and preserving all parsed key/value entries.

Observation shows which source used one route. It does not prove that the source is trustworthy. Never widen the result to a Docker, LAN, or tailnet supernet merely to make a rejected request succeed.

## Doctor

```bash
velociportal doctor --env-file .env
velociportal doctor --env-file .env \
  --identity alice@example.com \
  --identity bob@example.com
```

Doctor reports stable `PASS`, `WARN`, and `FAIL` stages for:

- configuration and owner-only environment-file permissions;
- trusted proxy CIDR narrowness;
- Headscale policy and node retrieval;
- NPM authentication and proxy-host retrieval;
- complete snapshot construction;
- supported ACL-to-`forward_host` join coverage; and
- optional matcher-backed identity previews.

Doctor sanitizes and bounds upstream errors. It does not print API keys, passwords, bearer tokens, or complete configuration structs. A passing run is a preflight result, not proof that rendered cards equal real network authorization.

## Validate

```bash
velociportal validate --env-file .env \
  --identity user-a=alice@example.com \
  --identity user-b=bob@example.com
```

Validation loads one complete live snapshot, compares the supplied identity labels through the same matcher used by the portal, and explains supported destination kinds. Summary privacy is the default: it emits opaque service IDs and never emits identity logins, domains, IPs, forward hosts, or raw selectors. `--privacy private` adds internal topology for local diagnosis and prints a warning to stderr.

Use `--format json` for deterministic, schema-versioned output. A complete report returns `1` when findings such as an unmatched forward host, a zero-card identity, identical card sets, or an unknown/dirty build require review. Notices about known limitations do not by themselves fail the report. GNU Make converts any failed recipe into Make exit code `2`, so automation that needs the application's exact `0`/`1`/`2` distinction should invoke `velociportal validate` directly; Make users should inspect the report status and findings.

Validation is still a visibility prediction. Follow the [real-deployment worksheet](../getting-started/validation.md) to verify the final identity proxy, `401`/`403` paths, browser-facing links, and actual Headscale reachability.

## Healthcheck

```bash
velociportal healthcheck
velociportal healthcheck --url http://127.0.0.1:8080/healthz --timeout 3s
```

The probe succeeds only on HTTP 200, refuses redirects, bounds response headers, and does not load application secrets. The production image invokes the same command directly through its Docker healthcheck.

## Canonical operator journey

```bash
make setup
make observe-proxy
make doctor
make up
make health
```

<div class="vp-chip-row" aria-label="Guided target availability">
<span class="vp-chip vp-chip--supported">Repository workflow implemented</span>
<span class="vp-chip vp-chip--security">Exact proxy confirmation required</span>
<span class="vp-chip vp-chip--validation">Real deployment validation still required</span>
</div>

| Guided step | What it runs |
|---|---|
| `make setup` | Builds the production image and runs the interactive setup wizard with a writable project mount |
| `make observe-proxy` | Runs the temporary observer on the production Compose network with a loopback-published port, then updates only the selected environment file |
| `make doctor` | Mounts the environment file read-only and runs preflight diagnostics on the production Compose network |
| `make validate` | Builds a human-readable two-or-more-identity validation report on the same production network |
| `make validate-json` | Keeps stdout JSON-only so it can be redirected to an owner-readable report file |
| `make up` | Builds the image, starts Compose without rebuilding, and waits for Docker health |
| `make health` | Executes the binary-native health client in the running container |

Use the full [Guided setup](../getting-started/setup.md) for the ordered workflow and final validation matrix.

## Other Make targets

| Target | Command role |
|---|---|
| `make build` | Build the local `velociportal` binary; requires Go |
| `make fmt-check` | Fail if Go files need formatting; requires Go |
| `make test` | Run race-enabled Go tests without test-result caching; requires Go |
| `make vet` | Run `go vet ./...`; requires Go |
| `make lint` | Run formatting and vet checks |
| `make check` | Run formatting, vet, and race tests |
| `make run` | Run from source with `serve --env-file`; requires Go |
| `make docker` | Build the production scratch image |
| `make docker-run` | Run the production image read-only with loopback-only publication |
| `make logs` | Follow Compose logs |
| `make down` | Stop the Compose deployment |
| `make verify` | Run Go, Compose, image metadata, and in-image CLI checks; requires Go and Docker |
| `make clean` | Remove the locally built binary |

## Make variables

Pass Make variables before or after the target:

```bash
make doctor DOCTOR_ARGS="--identity ${VP_USER_A} --identity ${VP_USER_B}"
make validate VALIDATE_ARGS="--identity user-a=${VP_USER_A} --identity user-b=${VP_USER_B}"
make health HEALTH_URL=http://127.0.0.1:8080/healthz
make docker IMAGE=registry.example/velociportal:test
```

For validation, populate `VP_USER_A` and `VP_USER_B` with hidden `read -s` prompts as shown in the [real-deployment validation guide](../getting-started/validation.md). This avoids literal logins in shell history, but command-line identities can still be observed by trusted same-host administration tools while the command runs.

| Variable | Default | Used for |
|---|---|---|
| `IMAGE` | `velociportal:latest` | Image build, Compose, and local container commands |
| `ENV_FILE` | `.env` | Project-relative environment-file path used by guided and runtime commands; absolute paths and `..` components are rejected by Make wrappers so host and container cannot address different files |
| `DOCTOR_ARGS` | empty | Additional doctor options, such as repeated identity previews |
| `VALIDATE_ARGS` | empty | Labeled identities plus optional privacy/format flags; never place credentials here |
| `BUILD_VERSION` | `dev` | Build provenance included in validation reports |
| `GIT_REVISION` | current Git `HEAD` | Source revision included in the binary and report |
| `GIT_SOURCE_STATE` | detected `clean` or `dirty` | Prevents dirty/unknown builds from being mistaken for source-traceable evidence |
| `HEALTH_URL` | `http://127.0.0.1:8080/healthz` | URL passed to the in-container health client |
| `VELOCIPORTAL_SUBNET` | `172.31.255.0/24` | Stable private Compose subnet; override only to resolve a route conflict |
| `VELOCIPORTAL_GATEWAY` | `172.31.255.1` | Stable host-side gateway paired with the selected subnet |
| `HOST_UID` / `HOST_GID` | current operator IDs | Native-Docker ownership for files written through the setup bind mount |
| `CONTAINER_UID` / `CONTAINER_GID` | host IDs, or `0:0` when rootless Docker is detected | Container identity for one-off tools; rootless container UID 0 maps to the unprivileged daemon owner |

Do not commit a populated environment file. Registry names and image tags are not secrets, but credentials, API keys, and passwords are.

## Runtime environment

| Variable | Required | Notes |
|---|---:|---|
| `HEADSCALE_URL` | Yes | Base URL without `/api/v1` |
| `HEADSCALE_API_KEY` | Yes | Headscale Bearer API key |
| `NPM_URL` | Yes | Use HTTPS unless traffic stays on an isolated local/container network |
| `NPM_EMAIL` | Yes | NPM account identity |
| `NPM_PASSWORD` | Yes | NPM account password |
| `LISTEN_ADDR` | No | Defaults to `127.0.0.1:8080`; Compose overrides it inside the container |
| `POLL_INTERVAL` | No | Go duration from `5s` through `24h`; defaults to `30s` |
| `TRUSTED_PROXY_CIDR` | Yes | Exact source `/32`, `/128`, or the smallest intentionally trusted proxy subnet |

The container listener and host publication serve different roles:

```yaml
# Inside the bridged container
environment:
  LISTEN_ADDR: 0.0.0.0:8080

# On the Docker host
ports:
  - "127.0.0.1:8080:8080"
```

`0.0.0.0` inside the container accepts bridged traffic. The loopback-only host publication prevents direct LAN access.

## Health endpoint

| Response | Meaning |
|---|---|
| `200` | A complete snapshot exists and is newer than three poll intervals |
| `503` | The snapshot is missing or stale |

The scratch image has no shell or separate HTTP utility. Its static binary contains the health client, and Docker runs it directly as `CMD ["/velociportal", "healthcheck"]`. Host-side `/healthz` probing remains useful as an independent monitor.

## Interpreting common failures

| Symptom | Check first |
|---|---|
| Startup rejects configuration | Missing required values, malformed URL/CIDR, or non-positive poll interval |
| `403 untrusted source` | Actual immediate peer seen inside the container versus `TRUSTED_PROXY_CIDR` |
| `401 no identity` | Whether the trusted proxy injected `Tailscale-User-Login` |
| `503` from health | Headscale policy, Headscale nodes, and NPM proxy hosts must all refresh successfully |
| Empty portal with healthy snapshot | Identity/group spelling, legacy `acls`, NPM `forward_host`, and documented matcher limits |

See [Known Limitations](known-limitations.md) before interpreting a healthy process as proof of policy parity.
