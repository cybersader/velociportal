# 02 — Design Decisions (Locked)

> Decisions that are settled. Revisit only with a stated reason. "Locked" means do not relitigate casually during implementation.

## D1 — Visibility layer only

Velociportal decides **what shows on the dashboard**, nothing more. Authentication, identity assertion, and access enforcement stay with Tailscale Serve, Headscale policy, NPM, and backend applications. Hiding a service card is UX, not authorization.

## D2 — Single-container deployment

Velociportal is one static non-root binary in one minimal container, with no application database and no persisted cache. Secrets come from administrator-managed environment files or secret facilities and are never baked into the image.

## D3 — One selected control plane plus NPM

`CONTROL_PLANE=headscale|tailscale` selects exactly one provider per process:

- Headscale loads `GET /api/v1/policy` and `GET /api/v1/node`.
- Tailscale SaaS loads OAuth plus `/tailnet/-/acl`, `/users`, and `/devices`.
- NPM remains outside the control-plane interface and supplies `POST /api/tokens` plus `GET /api/nginx/proxy-hosts`.

A single polling loop publishes only after one complete neutral control-plane result plus NPM succeeds. Multi-tailnet aggregation, an application database, and persisted cache remain deferred. Metadata must not become a second authorization model.

## D4 — Human identity comes from Tailscale HTTP Serve

The canonical browser path is the existing host-network TrueNAS Tailscale app with declarative HTTP Serve:

```text
Tailnet HTTP :8081 -> http://127.0.0.1:18080
```

WireGuard protects the client-to-NAS path from ordinary on-path LAN/router/ISP interception. Tailscale Serve strips caller-supplied `Tailscale-User-*` headers and injects the authenticated human identity. Velociportal accepts those headers only from `TRUSTED_PROXY_CIDR`.

NPM is not the portal identity provider. Velociportal has no direct Authentik, Authelia, `Remote-User`, or `X-Webauth-*` adapter. Endpoint compromise, Tailscale/Headscale control-plane compromise, and trusted-host compromise remain in scope.

## D5 — Go, server-rendered HTML, embedded htmx

Use Go 1.22+, standard-library HTTP/JSON primitives, embedded server-rendered HTML, and an embedded htmx asset. Authorization-like visibility filtering happens server-side before HTML is rendered. Add dependencies only when necessary.

## D6 — Narrow normalized access-rule matching

Headscale remains legacy-ACL-only in `legacy_acl_visibility_v1`. Tailscale may combine legacy `acls` `accept` rules with a narrow network Grants subset; accepted non-empty Grants select `network_access_visibility_v1`. Both rule kinds normalize into provenance-aware access rules and resolve supported destinations against NPM `forward_host`.

Grant `ip` capabilities accept wildcard, port/range, protocol wildcard, and protocol port/range forms. A Grant becomes HTTP card evidence only when one capability permits TCP to the exact NPM `forward_port`. Valid non-TCP-only Grants load but produce no card. Human logins, defined groups, wildcard, and Users-API-authoritative human-role autogroups may become Grant browser evidence. Valid tags, IPs, CIDRs, host aliases, `autogroup:shared`, `autogroup:tagged`, and other machine selectors may load but never map to a human browser identity; source classification is retained separately from the raw strings. Attr-only `nodeAttrs` accept only `*`, individual users, defined groups, tags, and `autogroup:member` targets with the `funnel` attribute as non-authorization metadata.

Locked exclusions:

- Legacy ACL ports/protocols remain unmodeled; SSH never becomes card evidence.
- Posture, IP sets, services, non-empty routing `via`, application capabilities, malformed capabilities, unknown semantics, and unsafe selectors reject the complete refresh.
- `autogroup:internet` fails closed.
- `tagOwners` and tags on owned nodes do not make a human a `tag:*` source.
- Users API role membership applies only to Grants. It does not authorize legacy ACL role sources or `nodeAttrs`, has no hierarchy, and is never inferred from devices, device owners, node tags, or `tagOwners`.
- NPM access lists are not visibility inputs.
- The `forward_host` join remains subject to real-deployment validation.

## D7 — Named private upstream bridge with egress

The canonical production bundle creates the normal user-defined Docker bridge `velociportal-upstreams`. TrueNAS catalog renderer library 2.3.4 replaces an app's implicit default network whenever any UI-managed network is selected. The `v0.1.0-rc.1` `internal: true` bridge therefore became Headscale's only network, removed outbound DNS/NAT, and caused mandatory DERP-map retrieval to fail. Live acceptance rolled that attachment back and established the `v0.1.0-rc.2` correction.

NPM always attaches through TrueNAS-managed settings with alias `npm.velociportal.internal`. Headscale mode additionally attaches Headscale with alias `headscale.velociportal.internal`; Tailscale SaaS mode needs no Headscale attachment and uses the preferred default network for fixed-origin HTTPS egress.

Velociportal uses:

```text
HEADSCALE_URL=http://headscale.velociportal.internal:8080
NPM_URL=http://npm.velociportal.internal:81
```

The bridge is egress-capable but does not publish attached container ports to the LAN. Velociportal explicitly prefers its fixed ingress bridge as the default route. Headscale port `8080` is container-exposed only (`None`/`Expose` in the accepted final TrueNAS configuration), never LAN-published. Untrusted containers must not join this bridge, and LAN-negative acceptance must rule out explicit publication or direct routing. Plain NPM HTTP is accepted only for the exact canonical alias or same-host/loopback compatibility routes; every other NPM location requires verified HTTPS. The base Compose bundle mounts no CA certificate.

## D8 — Exact Headscale HTTP allowlist; verified HTTPS everywhere else

Configuration validation accepts Headscale HTTP only for the exact local/internal host forms encoded in the implementation. The named private Docker alias is the canonical production case. Headscale URLs outside that allowlist require normal verified HTTPS.

Credentialed transports remain isolated, ignore environment proxies, refuse redirects, require bounded responses, and have no certificate-verification bypass. HTTP acceptance is not proof that the Docker/host route is actually private; setup, doctor, validation, and acceptance documentation must retain that caveat.

## D9 — Existing NPM is the Headscale-mode pre-tailnet HTTPS boundary

When Headscale is selected, brand-new clients need a trusted HTTPS Headscale control URL before they can join the tailnet. Existing NPM provides that endpoint using split-horizon/private DNS plus the operator's existing automated DNS-01 wildcard-certificate lifecycle, then proxies to Headscale over `velociportal-upstreams`. Do not publish the exact Headscale hostname/address in public DNS or issue an exact-host public leaf certificate that discloses it through certificate-transparency logs.

This makes NPM an explicit trust and availability boundary:

- NPM can observe Headscale control traffic and operator Bearer API keys.
- Preserve WebSocket and HTTP upgrade behavior.
- Do not enable authorization-header or full-request-header logging.
- Back up and restore NPM configuration and certificate state with the rest of the application state.
- If the NPM certificate is not already trusted by a joining client, stop rather than disabling verification.

Use separate Headscale API keys for workstation operators and Velociportal runtime. Runtime Velociportal bypasses NPM and reaches Headscale directly over the private bridge. `headscale-ops` remains workstation-only and HTTPS-only.

## D10 — Native Headscale HTTPS/private CA is optional

Direct native Headscale HTTPS remains an optional alternative through the optional Compose CA overlay. It adds no PKI daemon or container and no insecure mode. The canonical TrueNAS path does not require a CA mount and does not prescribe manual CA creation.

## D11 — Tailnet HTTP Serve is acceptable; native HTTPS Serve is future work

Official Tailscale can automate `*.ts.net` certificates. Headscale automatic HTTPS Serve remains upstream work tracked by [issue #2527](https://github.com/juanfont/headscale/issues/2527) and [PR #3300](https://github.com/juanfont/headscale/pull/3300). Tailnet HTTP Serve over WireGuard is therefore not a release blocker.

## D12 — Router replacement restores only ordinary network state

No CA private key, certificate lifecycle, application database, policy file, or durable service configuration lives on pfSense/the router. Router replacement requires restoring ordinary DNS and routing. Durable Headscale, NPM, Docker-network, policy, and application state stays on TrueNAS and in backups.

## D13 — No public support claim before real acceptance

Unit tests, fixtures, Compose rendering, and documentation builds are not production acceptance. Headscale support requires the canonical TrueNAS path to pass trusted NPM HTTPS checks, bootstrap/key separation, two distinct human identities, header replacement, LAN-negative raw-port tests, restart recovery, NPM join review, and comparison with actual reachability. Tailscale SaaS must remain preview until live OAuth scopes, token refresh/revocation, authoritative exact-login role mapping, direct-member/shared-user and no-hierarchy negatives, separate device-owner mapping, role-derived card evidence, unsupported-policy negatives, two identities, and actual reachability also pass.

## D14 — Tailscale SaaS is fixed-origin OAuth-only preview

Production uses `https://api.tailscale.com/api/v2`, the credential's `-` alias, and exactly `policy_file:read`, `devices:posture_attributes:read`, `devices:core:read`, and `users:read`. Add no API-key fallback, access-token environment variable, configurable API origin, explicit tailnet, insecure TLS, redirect following, or environment-proxy behavior.

Access tokens remain in memory, refresh about five minutes early, coalesce concurrent refresh, and retry one request after `401`. Client IDs, secrets, rejected tokens, replacement tokens, and encoded forms are redaction inputs. The complete users response is authoritative for Grant-role membership: required canonical `type` and `role` fields are mapped by exact `loginName`; direct members receive `autogroup:member` plus exactly their API role, shared users receive none, and unknown or malformed values fail closed. There is no role hierarchy or device-derived inference. Device-owner conversion remains separate. Users/devices conversion rejects blank, duplicate, ambiguous, unresolved, paginated, or partial data rather than publishing an incomplete snapshot.

## D15 — Provider switching is explicit and atomic

Setup prompts for the provider first and only that provider's credentials. New files always store `CONTROL_PLANE`. Existing v0.2 Headscale files without the selector remain compatible only with a value-free deprecation warning; v0.3 requires explicit selection.

When a provider switch would remove inactive known credential keys, setup lists the key names and requires explicit confirmation. Refusal or input abort leaves the file byte-for-byte unchanged. Unknown keys remain, and setup never creates a plaintext credential backup. Runtime ignores inactive known credentials but warns only by variable name.

## D16 — Validation schema v3 records access-rule provenance

Validation reports include non-sensitive `provider`, `policy_mode`, `support_level`, explicit/implicit selection, `access_rules`, and per-match `rule_kind` plus original `rule_index`. Headscale reports `supported`; Tailscale reports `preview`. Reports and Doctor never print tailnet data, OAuth client IDs, secrets, access tokens, Headscale keys, NPM credentials, or JWTs.
