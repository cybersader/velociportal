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

- Legacy ACL ports/protocols remain unmodeled; SSH never becomes HTTP service-card evidence. The separate Tailscale-preview Machines projection is governed by D20.
- Posture, IP sets, services, non-empty routing `via`, application capabilities, malformed capabilities, unknown semantics, and unsafe selectors reject the complete refresh.
- `autogroup:internet` fails closed.
- `tagOwners` and tags on owned nodes do not make a human a `tag:*` source.
- Users API role membership applies only to Grants and the bounded SSH Machines projection. It does not authorize legacy ACL role sources or `nodeAttrs`. The Owner inherits `autogroup:admin` exactly as Tailscale reports; specialized roles do not imply one another. Membership is never inferred from devices, device owners, node tags, or `tagOwners`.
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

Unit tests, fixtures, Compose rendering, and documentation builds are not production acceptance. Headscale support requires the canonical TrueNAS path to pass trusted NPM HTTPS checks, bootstrap/key separation, two distinct human identities, header replacement, LAN-negative raw-port tests, restart recovery, NPM join review, and comparison with actual reachability. Tailscale SaaS must remain preview until live OAuth scopes, token refresh/revocation, authoritative exact canonical-login role mapping with padded-login rejection, Owner-to-Admin automatic membership, direct-member/shared-user and specialized-role isolation negatives, separate device-owner mapping, role-derived service and SSH-machine evidence, unsupported HTTP-policy and SSH-suppression negatives, two identities, copied-target parity, and actual HTTP/Tailscale-SSH reachability also pass.

## D14 — Tailscale SaaS is fixed-origin OAuth-only preview

Production uses `https://api.tailscale.com/api/v2`, the credential's `-` alias, and exactly `policy_file:read`, `devices:posture_attributes:read`, `devices:core:read`, and `users:read`. Add no API-key fallback, access-token environment variable, configurable API origin, explicit tailnet, insecure TLS, redirect following, or environment-proxy behavior.

Access tokens remain in memory, refresh about five minutes early, coalesce concurrent refresh, and retry one request after `401`. Client IDs, secrets, rejected tokens, replacement tokens, and encoded forms are redaction inputs. The complete users response is authoritative for Grant and bounded SSH-machine role membership: required canonical `type` and `role` fields are mapped by exact unpadded `loginName`; direct members receive `autogroup:member` plus their API role, the Owner additionally receives `autogroup:admin`, shared users receive none, and unknown, malformed, or padded values fail closed. Specialized roles do not imply one another, and device-derived inference is forbidden. Device-owner conversion remains separate. Users/devices conversion rejects blank, duplicate, ambiguous, unresolved, paginated, or partial data rather than publishing an incomplete snapshot.

## D15 — Provider switching is explicit and atomic

Setup prompts for the provider first and only that provider's credentials. New files always store `CONTROL_PLANE`. Existing v0.2 Headscale files without the selector remain compatible only with a value-free deprecation warning; v0.3 requires explicit selection.

When a provider switch would remove inactive known credential keys, setup lists the key names and requires explicit confirmation. Refusal or input abort leaves the file byte-for-byte unchanged. Unknown keys remain, and setup never creates a plaintext credential backup. Runtime ignores inactive known credentials but warns only by variable name.

## D16 — Validation schema v3 records access-rule provenance

Validation reports include non-sensitive `provider`, `policy_mode`, `support_level`, explicit/implicit selection, `access_rules`, and per-match `rule_kind` plus original `rule_index`. Headscale reports `supported`; Tailscale reports `preview`. Reports and Doctor never print tailnet data, OAuth client IDs, secrets, access tokens, Headscale keys, NPM credentials, or JWTs.

## D17 — NPM names first; versioned metadata is presentation-only

For an already policy-matched NPM proxy host, Velociportal uses the first valid concrete NPM domain as the automatic card name and browser host, even when a wildcard appears earlier. Adding the real concrete hostname to the same NPM proxy host is the preferred correction. A separate duplicate NPM host may create duplicate cards.

A wildcard-only host remains visible but non-clickable. Velociportal never invents a wildcard URL, substitutes a root domain, or silently drops the service. A strict optional versioned JSON file applies only to an existing positive NPM proxy-host ID. Version 1 remains compatible for displayed-name/browser-URL overrides. Version 2 also accepts a canonical category and bounded non-negative integer order. Once any category/order exists in the complete metadata document, categorized cards sort by category with uncategorized last; explicit order sorts only within a category, followed by deterministic case-insensitive name and proxy-host-ID fallbacks. The file loads into the complete snapshot, reloads atomically, and fails before upstream contact when configured but invalid.

Metadata cannot create, hide, or enable a card, repair a host with no NPM domain, change `forward_host` or `forward_port`, replace access-rule evidence, alter health, or grant visibility. Unknown IDs produce only counts. The base image and production stack remain mount-free; the opt-in read-only overlay uses a fixed target and supplemental numeric group so TrueNAS permissions stay `950:950`, directory `0750`, and file `0640` without ownership or mode changes.

A guarded browser-local preference may hide only the fixed built-in Velociportal logo and defaults to visible. Arbitrary per-service logos, access-history collection, and server-side or account-synchronized personalization remain deferred. NPM `meta.nginx_online` is route/configuration state, not backend health. Future DNS/Tailscale hostname observations may become privacy-minimized, memory-only operator suggestions, but never authorization evidence or silent persisted mappings.

## D18 — Service health is explicit, direct-backend, and independent

Velociportal probes only positive NPM proxy-host IDs listed in a strict versioned operator file. The current enabled NPM `forward_scheme`, `forward_host`, and `forward_port` plus a configured HTTP path define the target; service-metadata/browser URLs are never probe inputs. HTTP uses credential-free `GET` with explicit accepted status ranges. TCP connects and closes without application payload.

Every DNS target requires an exact host or suffix allowlist match and every resolved address must fit an explicit CIDR. The complete answer set is validated before direct-IP dialing so mixed answers fail closed while HTTP Host and TLS SNI retain the original name. Unspecified, loopback, multicast, link-local, broadcast, IPv4-mapped bypasses, and exact/aliased NPM or selected-control-plane API sockets remain denied. Probe transports share no credentials, cookies, clients, or transports with control-plane/NPM APIs; redirects and environment proxies are disabled, headers and time are bounded, and HTTPS uses normal TLS 1.2+ verification.

One non-overlapping scheduler uses a fixed worker pool and an independent atomic memory-only result map. Invalid health configuration starts no new probes and prior observations age to stale. Health joins by proxy-host ID only after normal identity/policy matching and never changes card creation, order, URL, link state, authorization, complete snapshot publication, cache freshness, or `/healthz`. Doctor may report one bounded cycle using only IDs, protocols, coarse states, durations/status classes, and counts.

## D19 — Hostname suggestions are one-shot, private, and operator-applied

`velociportal suggest-hostnames` may inspect only names returned by the normal selected-control-plane node/device call plus optional bounded hostname-only stdin. Provider-visible names are untrusted input and do not prove association with an NPM backend; the private review and manual merge must verify every destination independently. It adds no endpoint, scope, DNS scan, log source, runtime store, portal route, persistence, or recurring observer. Candidate records retain only a canonical ASCII FQDN and coarse source class for the lifetime of the process.

Only enabled, identity-independent structurally matched, wildcard-only NPM hosts without an active metadata URL are eligible. A candidate must match an exact wildcard suffix on a DNS label boundary. Candidate/ID edges form a bipartite graph; only connected components with exactly one hostname and one NPM proxy-host ID may produce a proposal. Every larger component is rejected in full without tie-breaking by source, suffix length, order, ID, or address.

The operator must request private mode, select the browser-facing HTTP/HTTPS scheme, review concrete hostnames/IDs, and type literal `yes`. Output is a strict service-metadata v1 fragment on stdout or a new atomic no-clobber `0600` file. The command never edits active metadata or attaches the proposal to `CacheData`, so generation cannot create, hide, reorder, enable, or authorize cards. Applying a proposal remains a separate manual review/merge action.

## D20 — SSH Machines is a separate dual-evidence Tailscale preview

The portal may render a Tailscale-only Machines section, but it remains a visibility projection rather than authentication, enforcement, reachability, or health. Projection availability requires both the Tailscale provider and a present, fully supported SSH policy. Headscale, absent SSH, and unsupported SSH omit the complete section; a supported projection with zero identity matches preserves an explicit empty state. HTTP service matching remains unchanged and never consumes SSH policy.

A device becomes a machine card only when all of these inputs agree:

1. the browser login is an exact canonical full login for a direct `type: member` user in the complete Users API response;
2. one fully supported normalized Tailscale `ssh` rule matches that login through an exact login, defined exact-login group, or authoritative human-role autogroup and matches the device through a tag or `autogroup:self`;
3. an independent safe network Grant matches the same exact browser identity and the device's validated Tailscale address and permits TCP/22; and
4. the device has either a canonical full `*.ts.net` name or a validated Tailscale CGNAT IPv4/Tailscale ULA IPv6 fallback target.

Legacy ACLs, including port-22-looking destinations, are never machine evidence. NPM proxy hosts, access lists, service metadata, organization, health observations, and browser URLs are not machine inputs. Shared users and machine-derived selectors never become humans; Owner-to-Admin automatic membership and specialized-role isolation remain authoritative and exact. Padded API `loginName` values reject the complete refresh rather than being normalized into membership.

The supported SSH subset is deliberately bounded to `accept`/`check`, exact human/group/role sources, tag/`autogroup:self` destinations, literal validated OS users or `autogroup:nonroot`, and an optional canonical one-minute-through-168-hour `checkPeriod` on `check`. Any unsupported SSH rule makes the projection unavailable and omits the complete Machines section; absent SSH is likewise unavailable. Doctor retains a coarse top-level unsupported reason but emits no per-identity machine previews unless the projection is available. Otherwise valid HTTP service cards and snapshot publication remain intact. This asymmetric handling prevents an unfamiliar SSH feature from broadening machines without turning a separate authorization surface into an HTTP availability failure.

Machine cards are non-linkable summaries. A copy command is emitted only for a validated literal account; `autogroup:nonroot` never invents one. Targets are restricted to canonical full `*.ts.net` names or validated Tailscale addresses so arbitrary device-reported FQDNs cannot redirect the copied command. HTML, JavaScript, and shell metacharacters remain escaped or rejected. Doctor exposes only state/reason/rule counts and per-identity machine counts, never device IDs, names, addresses, accounts, or commands. Live two-identity card/command/reachability parity is required while Tailscale remains preview.
