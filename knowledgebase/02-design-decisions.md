# 02 — Design Decisions (Locked)

> Decisions that are settled. Revisit only with a stated reason. "Locked" means do not relitigate casually during implementation.

## D1 — Visibility layer only

Velociportal decides **what shows on the dashboard**, nothing more. Authentication, identity assertion, and access enforcement stay with Tailscale Serve, Headscale policy, NPM, and backend applications. Hiding a service card is UX, not authorization.

## D2 — Single-container deployment

Velociportal is one static non-root binary in one minimal container, with no application database and no persisted cache. Secrets come from administrator-managed environment files or secret facilities and are never baked into the image.

## D3 — Headscale and NPM remain the data sources

The runtime reads:

- Headscale `GET /api/v1/policy` and `GET /api/v1/node`.
- NPM `POST /api/tokens` and `GET /api/nginx/proxy-hosts`.

A single polling loop builds a complete in-memory snapshot. It publishes only when policy, nodes, and proxy hosts all succeed. Metadata added later must not become a second authorization model.

## D4 — Human identity comes from Tailscale HTTP Serve

The canonical browser path is the existing host-network TrueNAS Tailscale app with declarative HTTP Serve:

```text
Tailnet HTTP :8081 -> http://127.0.0.1:18080
```

WireGuard protects the client-to-NAS path from ordinary on-path LAN/router/ISP interception. Tailscale Serve strips caller-supplied `Tailscale-User-*` headers and injects the authenticated human identity. Velociportal accepts those headers only from `TRUSTED_PROXY_CIDR`.

NPM is not the portal identity provider. Velociportal has no direct Authentik, Authelia, `Remote-User`, or `X-Webauth-*` adapter. Endpoint compromise, Tailscale/Headscale control-plane compromise, and trusted-host compromise remain in scope.

## D5 — Go, server-rendered HTML, embedded htmx

Use Go 1.22+, standard-library HTTP/JSON primitives, embedded server-rendered HTML, and an embedded htmx asset. Authorization-like visibility filtering happens server-side before HTML is rendered. Add dependencies only when necessary.

## D6 — Legacy ACL matching only

Velociportal evaluates legacy `acls` entries with `action: "accept"`. It resolves supported user/group sources and destination hosts, CIDRs, policy aliases, destination tags, wildcard, and `autogroup:self` against NPM `forward_host`.

Locked exclusions:

- Grants, SSH, posture, capabilities, protocols, and ports are not modeled for visibility.
- `autogroup:internet` fails closed.
- `tagOwners` and tags on owned nodes do not make a human a `tag:*` source.
- NPM access lists are not visibility inputs.
- The `forward_host` join remains subject to real-deployment validation.

## D7 — Named internal upstream network

The canonical production bundle creates the Docker network `velociportal-upstreams`. Existing apps attach through TrueNAS-managed network settings with these exact aliases:

- Headscale: `headscale.velociportal.internal`
- NPM: `npm.velociportal.internal`

Velociportal uses:

```text
HEADSCALE_URL=http://headscale.velociportal.internal:8080
NPM_URL=http://npm.velociportal.internal:81
```

Headscale port `8080` is container-exposed only (`None`/`Expose` in the TrueNAS app), never LAN-published. Untrusted containers must not join this network. Plain NPM HTTP is accepted only for the exact canonical alias or same-host/loopback compatibility routes; every other NPM location requires verified HTTPS. The base Compose bundle mounts no CA certificate.

## D8 — Exact Headscale HTTP allowlist; verified HTTPS everywhere else

Configuration validation accepts Headscale HTTP only for the exact local/internal host forms encoded in the implementation. The named internal alias is the canonical production case. Headscale URLs outside that allowlist require normal verified HTTPS.

Credentialed transports remain isolated, ignore environment proxies, refuse redirects, require bounded responses, and have no certificate-verification bypass. HTTP acceptance is not proof that the Docker/host route is actually private; setup, doctor, validation, and acceptance documentation must retain that caveat.

## D9 — Existing NPM is the pre-tailnet Headscale HTTPS boundary

Brand-new clients need a trusted HTTPS Headscale control URL before they can join the tailnet. Existing NPM provides that endpoint using split-horizon/private DNS plus the operator's existing automated DNS-01 wildcard-certificate lifecycle, then proxies to Headscale over `velociportal-upstreams`. Do not publish the exact Headscale hostname/address in public DNS or issue an exact-host public leaf certificate that discloses it through certificate-transparency logs.

This makes NPM an explicit trust and availability boundary:

- NPM can observe Headscale control traffic and operator Bearer API keys.
- Preserve WebSocket and HTTP upgrade behavior.
- Do not enable authorization-header or full-request-header logging.
- Back up and restore NPM configuration and certificate state with the rest of the application state.
- If the NPM certificate is not already trusted by a joining client, stop rather than disabling verification.

Use separate Headscale API keys for workstation operators and Velociportal runtime. Runtime Velociportal bypasses NPM and reaches Headscale directly over the internal network. `headscale-ops` remains workstation-only and HTTPS-only.

## D10 — Native Headscale HTTPS/private CA is optional

Direct native Headscale HTTPS remains an optional alternative through the optional Compose CA overlay. It adds no PKI daemon or container and no insecure mode. The canonical TrueNAS path does not require a CA mount and does not prescribe manual CA creation.

## D11 — Tailnet HTTP Serve is acceptable; native HTTPS Serve is future work

Official Tailscale can automate `*.ts.net` certificates. Headscale automatic HTTPS Serve remains upstream work tracked by [issue #2527](https://github.com/juanfont/headscale/issues/2527) and [PR #3300](https://github.com/juanfont/headscale/pull/3300). Tailnet HTTP Serve over WireGuard is therefore not a release blocker.

## D12 — Router replacement restores only ordinary network state

No CA private key, certificate lifecycle, application database, policy file, or durable service configuration lives on pfSense/the router. Router replacement requires restoring ordinary DNS and routing. Durable Headscale, NPM, Docker-network, policy, and application state stays on TrueNAS and in backups.

## D13 — No public support claim before real acceptance

Unit tests, fixtures, Compose rendering, and documentation builds are not production acceptance. Public support requires the canonical TrueNAS path to pass trusted NPM HTTPS checks, bootstrap/key separation, two distinct human identities, header replacement, LAN-negative raw-port tests, restart recovery, NPM join review, and comparison with actual Headscale reachability.
