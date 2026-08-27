# Local-source and diagnostic workflow

This page documents repository commands for contributors, local builds, preflight diagnosis, exact proxy-source observation, and advanced validation. It is **not** the normal TrueNAS installation path.

<div class="vp-chip-row" aria-label="Setup scope">
<span class="vp-chip vp-chip--supported">Local source workflow</span>
<span class="vp-chip vp-chip--security">Exact proxy observation</span>
<span class="vp-chip vp-chip--validation">Advanced diagnostics</span>
</div>

!!! tip "Deploying on TrueNAS?"
    Start with the [TrueNAS Quickstart](../guides/truenas-scale.md). It uses a published immutable image, TrueNAS-managed networks and aliases, no source build on the NAS, and no recurring NAS shell.

!!! warning "What this workflow does not prove"
    A healthy process proves only that Velociportal has a recent complete snapshot. It does not prove that an allowlisted Headscale HTTP route is actually private, that NPM preserves the Headscale control protocol, that identity headers are trustworthy, or that cards equal real reachability.

## Repository command path

```text
make setup
make observe-proxy
make doctor
make up
make health
```

After health succeeds, use `make validate` for join evidence and complete the real two-identity worksheet.

## Before you begin

You need:

- A local Git checkout, GNU Make, Go 1.22+, Docker Engine **28.0+**, and Docker Compose **2.33.1+**.
- Reachable Headscale and NPM endpoints from the container network used by this local workflow.
- A trusted Tailscale Serve or other supported proxy path that strips caller-supplied identity headers and injects `Tailscale-User-Login`.
- A private path from that identity source to the loopback-published application port.

The canonical production runtime values are:

```text
HEADSCALE_URL=http://headscale.velociportal.internal:8080
NPM_URL=http://npm.velociportal.internal:81
```

Those aliases work only when the relevant containers are attached to `velociportal-upstreams`. A local source checkout may instead use verified HTTPS for Headscale and NPM endpoints reachable from its own Compose network.

Headscale and NPM HTTP are accepted only for their exact canonical private Docker aliases or same-host/loopback compatibility routes. Every other location requires verified HTTPS.

!!! danger "Do not start by exposing port 8080"
    The repository Compose deployment publishes `127.0.0.1:8080:8080`. Require Docker Engine 28+ and keep that loopback boundary. A raw LAN path lets callers attempt to forge identity headers.

!!! warning "The Docker host is inside this trust boundary"
    Docker NAT can present host-originated traffic as the bridge gateway. A host process or host-network container that can reach loopback may therefore share the trusted source used by Tailscale Serve. The production acceptance worksheet treats this as an explicit host trust boundary.

## 1. Prepare configuration

Run:

```bash
make setup
```

The wizard reads API keys and passwords through hidden terminal input, atomically writes an owner-only environment file, preserves unknown keys, and intentionally leaves `TRUSTED_PROXY_CIDR` unset until observation.

| Variable | Purpose | Safe guidance |
|---|---|---|
| `HEADSCALE_URL` | Headscale base URL | Exact allowlisted local/internal HTTP or verified HTTPS elsewhere; no `/api/v1` |
| `HEADSCALE_API_KEY` | Runtime Bearer key | Use a key distinct from the workstation operator key |
| `NPM_URL` | NPM base URL | Exact internal/same-host HTTP allowlist; verified HTTPS elsewhere |
| `NPM_EMAIL` | NPM login identity | Prefer a dedicated account that can list proxy hosts |
| `NPM_PASSWORD` | NPM login secret | Never commit it |
| `LISTEN_ADDR` | Process listener | Compose sets `0.0.0.0:8080` inside the container |
| `POLL_INTERVAL` | Refresh cadence | `5s` through `24h`; default `30s` |
| `TRUSTED_PROXY_CIDR` | Source allowed to assert identity | Fill after observing the real path |

When Headscale uses verified HTTPS with a private root, activate the optional repository overlay with the **public** root only:

```bash
export PRIVATE_CA_FILE="$HOME/.local/share/velociportal/certs/rootCA.pem"
```

The base Compose file has no CA mount. Never set `PRIVATE_CA_FILE` to a CA private key, leaf private key, directory, or combined private-key bundle.

The setup wizard warns when an allowlisted Headscale HTTP URL is selected because configuration validation cannot prove Docker/host route confinement or external inaccessibility.

## 2. Observe the trusted proxy source

Run:

```bash
make observe-proxy
```

The observer creates a random one-time path, records only the immediate TCP peer, ignores forwarded and identity headers, and proposes an exact `/32` or `/128`. Append the path to the final identity-aware browser origin rather than browsing directly to the temporary listener.

Observation shows which source used one route; it does not prove that the source is trustworthy. Do not widen the result to a Docker, LAN, or tailnet supernet to make a request succeed.

## 3. Run preflight checks

```bash
make doctor
```

Optional identity previews:

```bash
make doctor DOCTOR_ARGS='--identity alice@example.com --identity bob@example.com'
```

Doctor verifies configuration, environment-file mode, upstream calls, complete snapshot construction, and join coverage. For allowlisted Headscale HTTP it emits a warning **before** contacting the upstream because it cannot prove the route is private.

Resolve every failed check and review every warning. In particular:

- Confirm Headscale HTTP uses only an exact intended local/internal host and that the real port is not LAN-published.
- Confirm verified HTTPS endpoints validate without an insecure flag.
- Confirm redirects and environment proxies are not required.
- Confirm NPM credentials do not cross a broader network over HTTP.
- Confirm `TRUSTED_PROXY_CIDR` is explicit and narrow.

A passing Doctor result does not establish NPM control-proxy behavior, identity injection, or reachability parity.

## 4. Build and start locally

```bash
make up
```

This contributor workflow deliberately builds the current source tree. The production TrueNAS journey does not. The resulting container is non-root, read-only, and `FROM scratch`.

If startup fails, confirm Headscale policy, Headscale nodes, and NPM proxy hosts can all be fetched. The cache publishes only after all three succeed.

## 5. Check snapshot health

```bash
make health
```

`GET /healthz` returns:

- **`200`** when a complete snapshot exists and is newer than three poll intervals.
- **`503`** when the snapshot is empty or stale.

A restart starts cold and requires a complete refresh.

## 6. Generate validation evidence

Use at least two real identities with intentionally different policy membership:

```bash
read -r -s -p 'Login for user-a: ' VP_USER_A; printf '\n'
read -r -s -p 'Login for user-b: ' VP_USER_B; printf '\n'
make validate VALIDATE_ARGS="--identity user-a=${VP_USER_A} --identity user-b=${VP_USER_B}"
unset VP_USER_A VP_USER_B
```

For allowlisted Headscale HTTP, validation emits a non-failing route-confinement notice and includes the limitation in the report without printing the URL or credential.

Summary reports remain topology-minimized, not anonymous. Private reports can expose internal hostnames, forward targets, and policy relationships and must not be published.

## Real acceptance still required

The local workflow cannot replace the canonical acceptance matrix. Before any support claim, verify:

| Test | Expected result |
|---|---|
| Trusted NPM Headscale HTTPS | Brand-new client and HTTPS-only `headscale-ops` succeed without insecure flags |
| NPM protocol behavior | Headscale WebSocket/upgrade behavior works and auth headers are not logged |
| Runtime upstream isolation | Headscale `8080` and Velociportal raw port are unreachable on the LAN address |
| User A through Serve | Only User A's supported matches appear |
| User B through Serve | A meaningfully different card set appears |
| Caller-supplied identity header | Serve strips or replaces it |
| Every card | Browser URL and actual Headscale/NPM reachability agree |
| Restart sequence | Velociportal, Tailscale, NPM, Headscale, and TrueNAS recover |
| Backup/restore | NPM certificate/config and Headscale/policy state can be restored |

Tailnet HTTP over WireGuard prevents ordinary on-path LAN/router/ISP interception, not endpoint, host, NPM, or control-plane compromise.

## Next steps

- [Return to the TrueNAS Quickstart](../guides/truenas-scale.md)
- [Generate and interpret a validation report](validation.md)
- [Understand the identity trust boundary](../reference/tailscale-headers.md)
- [Review every known limitation](../reference/known-limitations.md)
- [Use the CLI and Make reference](../reference/cli.md)
