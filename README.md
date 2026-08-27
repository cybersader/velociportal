<div align="center">

<img src="assets/logo-card.svg" alt="Velociportal raptor logo" width="144" height="144" />

# Velociportal

### Your network access policy *is* your dashboard policy.

A self-hosted service portal that reads **Headscale legacy ACL rules** and **Nginx Proxy Manager proxy hosts**, then renders only the cards its current matcher can correlate with the trusted viewer identity.

**Visibility layer only — complements identity and enforcement systems; never replaces them.**

[![CI](https://github.com/cybersader/velociportal/actions/workflows/ci.yml/badge.svg)](https://github.com/cybersader/velociportal/actions/workflows/ci.yml)
[![Docs](https://github.com/cybersader/velociportal/actions/workflows/docs.yml/badge.svg)](https://github.com/cybersader/velociportal/actions/workflows/docs.yml)

[TrueNAS Quickstart](https://cybersader.github.io/velociportal/guides/truenas-scale/) · [Documentation](https://cybersader.github.io/velociportal/) · [Optional private TLS](https://cybersader.github.io/velociportal/guides/private-tls/) · [Known limitations](https://cybersader.github.io/velociportal/reference/known-limitations/) · [Roadmap](#roadmap)

</div>

---

## Start here

Follow the [TrueNAS Quickstart](https://cybersader.github.io/velociportal/guides/truenas-scale/). It is the single UI-managed path for:

1. Importing the one-container Compose bundle so the named private upstream bridge exists.
2. Attaching Headscale and NPM with exact private Docker aliases, preserved outbound DNS, and no LAN-published Headscale API port.
3. Using the operator's existing trusted NPM HTTPS certificate lifecycle for pre-tailnet Headscale control and workstation administration.
4. Bootstrapping one short-lived API key, then separating operator and Velociportal runtime keys.
5. Configuring a real legacy ACL exercise and declarative Tailscale HTTP Serve.
6. Deploying Velociportal without a source build or recurring NAS shell.
7. Running two-identity, header-replacement, restart, join, reachability, and LAN-negative acceptance.

The production bundle lives under [`deploy/`](./deploy/). It requires Docker Compose 2.33.1+ and Docker Engine 28+, pulls one immutable published image, creates `velociportal-upstreams`, runs exactly one Velociportal container, and never builds source on the deployment host. The base stack mounts no CA certificate; the private-CA overlay is optional.

> [!IMPORTANT]
> No usable public image, `headscale-ops` release, or support claim is implied until tagged artifacts are actually published, anonymously verified, and the real TrueNAS acceptance matrix passes.

> [!WARNING]
> The canonical browser route is tailnet-only HTTP Serve over WireGuard: `:8081 -> http://127.0.0.1:18080`. NPM is not portal identity. Official Tailscale can automate `*.ts.net` certificates, but Headscale automatic HTTPS Serve remains future upstream work tracked by [issue #2527](https://github.com/juanfont/headscale/issues/2527) and [PR #3300](https://github.com/juanfont/headscale/pull/3300). Tailnet HTTP Serve is not a release blocker.

## Approved deployment architecture

```mermaid
flowchart LR
    Client["New or existing client"] -->|"trusted HTTPS"| NPMControl["Existing NPM<br/>Headscale control proxy"]
    NPMControl -->|"private HTTP<br/>WebSocket/upgrade preserved"| HS["Headscale"]
    HS -->|"runtime API<br/>private HTTP"| VP["Velociportal<br/>snapshot + matcher"]
    NPM["NPM proxy-host API"] -->|"private HTTP"| VP
    Human["Human tailnet user"] -->|"WireGuard"| Serve["Tailscale HTTP Serve<br/>human identity headers"]
    Serve -->|"host loopback"| VP
    VP --> Portal["Per-user portal"]
    Portal -. "service traffic" .-> NPM
```

The named private Docker bridge is `velociportal-upstreams`:

```text
HEADSCALE_URL=http://headscale.velociportal.internal:8080
NPM_URL=http://npm.velociportal.internal:81
```

Headscale and NPM HTTP are accepted only for their exact canonical private aliases or same-host/loopback compatibility routes. Every other location requires verified HTTPS. Credentialed clients refuse redirects, ignore environment proxy variables, and bound response sizes. There is no insecure TLS mode.

Existing NPM provides the trusted HTTPS endpoint that brand-new clients need before they can join the tailnet. The canonical privacy-preserving form uses split-horizon/private DNS and an existing publicly trusted wildcard certificate obtained with DNS-01, without a public Headscale hostname/address record or exact-host certificate-transparency disclosure. This project does not prescribe manual CA creation as the canonical path. If that endpoint is not already trusted by a client, stop rather than disabling verification.

NPM is therefore an explicit trust and availability boundary. It can observe Headscale control traffic and workstation operator Bearer API keys. Preserve WebSocket/upgrade behavior, avoid authorization-header logging, back up NPM state, and use separate Headscale operator and Velociportal runtime keys. Runtime Velociportal bypasses NPM and uses the private bridge directly. `headscale-ops` remains workstation-only and HTTPS-only.

## How the portal works

Velociportal polls three current inputs on one ticker:

1. **Headscale** `GET /api/v1/policy` — `groups`, `tagOwners`, legacy `acls`, and `hosts`
2. **Headscale** `GET /api/v1/node` — node owners, IPs, and tags for destination resolution
3. **NPM** `GET /api/nginx/proxy-hosts` — domains, forward targets, enabled state, and online metadata

A refresh replaces the cache only after all three calls succeed. Requests use the last complete in-process snapshot and never wait on an upstream API.

On each request, Velociportal accepts `Tailscale-User-Login` only from `TRUSTED_PROXY_CIDR`, resolves supported identity and group forms, evaluates supported legacy ACL `accept` rules against enabled NPM proxy hosts, and renders matching cards server-side. Headscale ACLs, Tailscale Serve, NPM, and backends still enforce access.

## What it is — and is not

| Velociportal is | Velociportal is not |
|---|---|
| A visibility layer derived from a supported Headscale policy subset | A login, SSO, OIDC, SAML, or MFA provider |
| A read-only Headscale and NPM API consumer | A reverse proxy or request enforcement point |
| One static Go binary in one minimal container | An ACL editor or service configuration database |
| An in-memory, all-or-nothing polling snapshot | A complete Tailscale policy engine |

## Current support boundary

**Implemented**

- Exact allowlisted local Headscale HTTP plus verified HTTPS elsewhere
- Separate hardened Headscale and NPM transports with no redirects or environment proxies and bounded responses
- Named private production bridge and exact Headscale/NPM aliases
- Optional private-CA public-root overlay with no CA mount in the base stack
- NPM credential JWT and proxy-host client
- All-or-nothing background snapshot refresh with atomic swap
- Trusted-source `Tailscale-User-*` identity middleware
- Legacy ACL-to-service matching for supported identity and destination forms
- Server-rendered responsive portal, embedded htmx refresh, and NPM status indicators
- Non-root `FROM scratch` image and Engine-28+-gated loopback-only publication
- Portable one-service production bundle and declarative Tailscale HTTP Serve template
- Explainable, privacy-controlled multi-identity validation reports with build provenance

**Not implemented**

- Tailscale SaaS API support
- Grants, SSH, posture, capabilities, port, or protocol evaluation
- Caddy or Traefik service discovery
- Direct Authentik, Authelia, `Remote-User`, or `X-Webauth-*` adapters
- NPM access-list-driven visibility
- Headscale automatic HTTPS Serve certificate automation

**Still requires real deployment validation**

- The NPM HTTPS-to-Headscale control path, including WebSocket/upgrade behavior and trusted certificate use by brand-new clients
- The private Docker network and proof that Headscale port `8080` is not reachable from the LAN
- Separate operator/runtime key handling and NPM header-logging posture
- Declarative Serve identity injection, header replacement, and restart persistence
- The NPM `forward_host` join against real tailnet-routable destinations
- At least two human identities with intentionally different card sets
- Every generated link compared with actual Headscale and NPM reachability

Tailnet HTTP over WireGuard prevents ordinary on-path LAN/router/ISP interception. It does not protect against compromised clients, TrueNAS, NPM, Tailscale/Headscale control components, or trusted host workloads.

Read the complete [Known Limitations](https://cybersader.github.io/velociportal/reference/known-limitations/) before deployment.

## Router and backup boundary

No CA state lives on pfSense/the router. Router replacement restores ordinary DNS and routing only. Durable Headscale data and policy, NPM database/configuration/certificates, TrueNAS app settings, Docker network configuration, Serve configuration, and Velociportal environment files stay on TrueNAS and in tested backups.

## Tech stack

| Layer | Choice |
|---|---|
| Language | Go 1.22, standard library HTTP/JSON/logging |
| Rendering | Embedded server-rendered HTML |
| Interactivity | Embedded htmx; no CDN and no SPA |
| Identity | `Tailscale-User-*` headers from trusted Tailscale Serve |
| State | Atomic in-memory snapshot; no application database |
| Container | Multi-stage build to a non-root `FROM scratch` image |
| Target | TrueNAS SCALE or another Docker host with Compose 2.33.1+ and Engine 28+ |

## Documentation paths

| Goal | Page | Status |
|---|---|---|
| Install the full private stack | [TrueNAS Quickstart](https://cybersader.github.io/velociportal/guides/truenas-scale/) | Canonical release-candidate journey |
| Understand control and runtime paths | [Headscale + NPM](https://cybersader.github.io/velociportal/guides/headscale-npm/) | Implemented architecture; live acceptance pending |
| Compare identities and joins | [Real deployment validation](https://cybersader.github.io/velociportal/getting-started/validation/) | Tooling implemented; live worksheet pending |
| Use native private Headscale TLS | [Optional private TLS](https://cybersader.github.io/velociportal/guides/private-tls/) | Alternative only |
| Build and diagnose from source | [Local-source workflow](https://cybersader.github.io/velociportal/getting-started/setup/) | Contributor/advanced diagnostics only |
| Review trust and spoofing controls | [Tailscale identity headers](https://cybersader.github.io/velociportal/reference/tailscale-headers/) | Required reading |

## Roadmap

- [x] Headscale policy and node API clients
- [x] NPM JWT auth and proxy-host client
- [x] Complete-snapshot cache with failure retention
- [x] Trusted-proxy identity middleware
- [x] Legacy ACL matcher and server-side visibility filtering
- [x] Responsive portal with embedded htmx
- [x] Minimal non-root container and loopback-only Compose examples
- [x] Explainable multi-identity validation reports
- [x] Hardened isolated upstream transports and exact Headscale HTTP allowlist
- [x] Named private upstream bridge with direct runtime aliases and required egress
- [x] Optional private-CA Compose overlay without a base-stack CA mount
- [x] Canonical NPM Headscale control-proxy and Tailscale Serve architecture documented
- [x] Publish and anonymously verify immutable Velociportal and `headscale-ops` release-candidate artifacts
- [ ] Complete real NPM control-proxy, bootstrap, key-separation, and backup acceptance
- [ ] Complete two-identity, LAN-negative, restart, join, link, and reachability acceptance
- [ ] Refine or replace the `forward_host` join
- [ ] Model ports, protocols, and Grants safely
- [ ] Derive browser-facing URLs from NPM frontend fields
- [ ] Add custom service metadata
- [ ] Add Tailscale SaaS, Caddy, and Traefik adapters

## License

[MIT](./LICENSE)
