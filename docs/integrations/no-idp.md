# No IdP / Tailscale identity only

This is the identity mode the current runtime supports: a human tailnet identity arrives through trusted `Tailscale-User-*` headers. Velociportal does not run a login flow or connect directly to an IdP.

## Headers

| Header | Use |
|---|---|
| `Tailscale-User-Login` | Required matching identity |
| `Tailscale-User-Name` | Optional display name |
| `Tailscale-User-Profile-Pic` | Optional; currently not rendered |

Tagged-device and Funnel requests do not receive human identity headers.

## Canonical publication

Use the existing TrueNAS Tailscale app with host networking and declarative HTTP Serve:

```text
Tailnet HTTP :8081 -> http://127.0.0.1:18080
```

Required conditions:

- Headscale policy allows intended users to reach the TrueNAS Tailscale IP on TCP `8081`.
- `serve.json` is mounted read-only and loaded through `TS_SERVE_CONFIG`.
- Docker Engine 28+ keeps `127.0.0.1:18080:8080` unreachable from the LAN.
- `TRUSTED_PROXY_CIDR` identifies only the immediate trusted source.
- Serve strips caller-supplied `Tailscale-User-*` headers and injects its authenticated values.

The browser URL is HTTP, while WireGuard protects client-to-NAS transport from ordinary on-path LAN/router/ISP interception. Endpoint, host, NPM, Tailscale/Headscale control-plane, and trusted-workload compromise remain in scope.

!!! danger "Header trust is the security boundary"
    A caller that can bypass Serve from inside the trusted CIDR can attempt to forge another user. Keep the raw port off the LAN, trust the host and local workloads, and validate header replacement with two real identities.

## NPM has a different role

Existing NPM provides the trusted HTTPS Headscale endpoint needed before clients join the tailnet and by HTTPS-only `headscale-ops`. It is not part of portal identity and cannot derive a human Tailscale login.

Runtime Velociportal bypasses NPM and reaches Headscale/NPM APIs directly over `velociportal-upstreams`. Keep Headscale operator and Velociportal runtime keys separate because NPM can observe the operator Bearer key after TLS termination.

## HTTPS Serve status

Official Tailscale can automate `*.ts.net` certificates. Headscale automatic HTTPS Serve remains future upstream work tracked by [issue #2527](https://github.com/juanfont/headscale/issues/2527) and [PR #3300](https://github.com/juanfont/headscale/pull/3300).

Tailnet HTTP Serve over WireGuard is the approved path and is not a release blocker. Do not substitute an NPM-only portal route or caller-supplied identity headers.

Follow the [TrueNAS Quickstart](../guides/truenas-scale.md), read the [identity-header trust boundary](../reference/tailscale-headers.md), and complete the [real validation worksheet](../getting-started/validation.md). No public support claim is warranted before acceptance passes.
