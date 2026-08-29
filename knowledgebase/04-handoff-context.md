# 04 — Handoff Context

> Hot context for whoever picks this up next. Stable reasoning remains in the other numbered knowledgebase documents; public operational truth lives under `docs/`.

## Current stage

**Published `v0.2.0-rc.4` is immutable at `ghcr.io/cybersader/velociportal@sha256:30a7567c169836e8ae6bbf6c2280227d403b52c79a760ee57a2764d154fae02d`.** Live TrueNAS use confirmed the corrected Owner-to-Admin automatic membership, a complete healthy snapshot, declarative Serve ingress, portal `200`, and 48 cards for the real Owner identity without changing the live tailnet policy.

That use exposed two narrower presentation defects: wildcard-only NPM `*.rader.wiki` became `https://%2A.rader.wiki/`, and NPM `meta.nginx_online` dots falsely implied backend applications were running. The merged truthful-card work fixes both and adds strict optional display-name/browser-URL metadata keyed by existing NPM proxy-host ID. Branch `feature/bounded-service-health` now implements explicit-opt-in direct-backend HTTP/TCP observations with strict topology allowlists, isolated credential-free transport, bounded scheduling, identity-filtered labels, and no authorization or `/healthz` effect.

The production topology remains one service on exactly two networks. NPM always uses the egress-capable `velociportal-upstreams`; Headscale mode also uses its private alias, while Tailscale SaaS uses the preferred default network for fixed-origin verified HTTPS egress. Browser ingress remains declarative Tailscale HTTP Serve on `:8081` to `http://127.0.0.1:18080`. The base bundle has no host mount; `compose.private-ca.yaml`, `compose.service-metadata.yaml`, and `compose.service-health.yaml` are independent optional overlays.

RC.4, RC.3, and every earlier published candidate remain immutable and must never be replaced or retagged. The current branch is repository-only work: no TrueNAS, NPM, Tailscale, DNS, dataset, permission, network, port, or policy changes have been made. Publication and any live app/overlay change require separate explicit approval. Two-identity role-card/reachability parity, token refresh/revocation, unsupported-policy negatives, stale/cold recovery, header replacement, and LAN-negative isolation remain pending.

## Locked direction

- Visibility layer only; never authentication or enforcement.
- Single non-root `FROM scratch` container.
- One selected control plane plus NPM: Headscale policy/nodes or Tailscale OAuth/policy/users/devices, plus NPM proxy hosts.
- Headscale mode: legacy ACL only in `legacy_acl_visibility_v1`. Tailscale preview: ACL-only policies use the legacy mode; accepted Grants select `network_access_visibility_v1`.
- New files select `CONTROL_PLANE` explicitly; absent selection is v0.2 Headscale compatibility only and warns before v0.3 enforcement.
- Browser identity: only trusted `Tailscale-User-*` headers from Tailscale Serve.
- Canonical browser ingress: tailnet HTTP `:8081` over WireGuard to loopback `127.0.0.1:18080`.
- Canonical local runtime upstreams: NPM and, in Headscale mode, Headscale over the private egress-capable bridge and exact aliases. Tailscale SaaS uses fixed-origin verified HTTPS over the preferred default network.
- Headscale outside the local HTTP allowlist: verified HTTPS only, with no redirects, environment proxies, or insecure verification.
- Tailscale: OAuth-only fixed origin, exact four read scopes, `-` alias, in-memory tokens, early/coalesced refresh, one retry after `401`, strict exact-login user type/role mapping, separate strict device-owner mapping, and complete client-ID/secret/token redaction.
- Headscale-mode pre-tailnet control/API: trusted NPM HTTPS endpoint reached through split-horizon/private DNS with an existing DNS-01 wildcard certificate, no public Headscale DNS record or exact-host certificate-transparency disclosure, WebSocket/upgrade preservation, and explicit NPM trust/availability exposure.
- Separate Headscale operator and Velociportal runtime keys.
- No application database; one atomic in-memory snapshot.
- No raw Velociportal or Headscale API port on the LAN.
- Native Headscale private-CA TLS is optional only; no PKI service.
- No CA or durable app state on the router.

## Implemented behavior

- **Provider core:** provenance-aware normalized access rules, whole-provider load interface, typed stages/errors, provider-gated fail-closed policy validation, support metadata, and cross-provider matcher parity fixtures.
- **Configuration:** `CONTROL_PLANE=headscale|tailscale`; absent selector defaults to Headscale with a v0.2 deprecation warning. Only active provider keys are required. Inactive known keys are ignored with key-name-only warnings and included in redaction inputs.
- **Setup:** provider-first prompts, Headscale supported/Tailscale preview labels, selected-family-only credentials, explicit selector output, confirmed deletion of inactive known keys on switch, unknown-key preservation, and byte-identical refusal/abort behavior.
- **Headscale client:** isolated transport with TLS 1.2 minimum when TLS is used, no environment proxy, no redirects, bounded headers/bodies, Bearer auth, policy wrapper parsing, limited huJSON normalization, and node parsing including current and legacy tag fields.
- **Tailscale client:** fixed verified production origin, exact four OAuth scopes, `-` tailnet alias, in-memory token reuse/refresh/concurrency, one retry after `401`, policy/users/devices load, strict canonical user `type`/`role` validation, exact-login Grant-role selector construction including Owner-to-Admin automatic membership, separate strict device-owner mapping, partial-response rejection, and credential/token redaction. Support level remains preview.
- **NPM client:** separate isolated transport with no environment proxy or redirects and bounded headers/bodies; credential JWT, proactive reauthentication, one retry on `401`, and proxy-host parsing.
- **Diagnostics:** Doctor reports explicit/implicit selection, provider-specific OAuth/API progress, access-rule counts, policy mode, support label, SSH separation, health-config state, and one bounded health cycle without printing client IDs, tokens, secrets, health paths/hosts/IPs, raw network errors, or tailnet data. Validation schema v3 records non-sensitive provider/policy/support/selection metadata plus `access_rules`, `rule_kind`, and original `rule_index`; it warns on implicit Headscale and retains the Headscale HTTP route notice.
- **Cache:** synchronous startup refresh followed by a ticker; per-stage timeouts; optional service metadata loads before upstream contact; publish only when metadata plus the complete selected-provider result and proxy hosts succeed; failed refresh keeps the exact previous complete snapshot. Health uses a separate scheduler and atomic result map and cannot change cache publication/freshness or `/healthz`.
- **Identity middleware:** reject sources outside `TRUSTED_PROXY_CIDR`; require `Tailscale-User-Login`; optional name/profile fields.
- **Matcher:** exact full-domain identity handling; short/bare legacy identities only when the trusted header itself is short/bare; groups, exact destinations, CIDRs, policy host aliases, destination tags, wildcard, and `autogroup:self`; authoritative role selectors are consulted only for Grant rules and only by exact login; Grant cards require TCP to the exact NPM backend port; one shared evidence-returning evaluator feeds portal cards and validation reports.
- **Fail-closed cases:** blank login, unsupported selectors/autogroups including `autogroup:internet`, posture, routing, services, IP sets, application capabilities, malformed capabilities, unknown semantics, and missing supported access-rule matches render no cards or reject the complete refresh as appropriate. Legacy ACL ports/protocols remain unmodeled; valid non-TCP Grants produce no HTTP card.
- **Authoritative roles without machine inference:** supported human-role autogroups map to a browser identity only through exact Users API membership and only for Grants. Machine/tag/IP/CIDR/host selectors, `autogroup:shared`, `autogroup:tagged`, and other machine autogroups may load but never become human; neither device ownership, `tagOwners`, nor tags on owned nodes confer source membership. Only recognized attr-only `funnel` `nodeAttrs` load, and they never authorize.
- **Portal:** embedded server-rendered HTML, escaped card content, HTTP/HTTPS scheme allowlist, first-concrete-NPM-domain selection, visible non-linkable wildcard cards, exact-ID display name/URL overrides, no NPM-derived health dots, identity-filtered accessible coarse health labels, responsive light/dark theme, and embedded htmx refresh.
- **Service health:** strict version-1 explicit target file; HTTP GET/TCP connect-only probes derived only from current NPM backends; exact host/suffix plus all-answer CIDR validation; direct validated-IP dialing with Host/SNI preservation; hard-denied address classes and protected API sockets; no credentials, proxies, redirects, payload inspection, or persistence; fixed workers, non-overlap, timeout/cycle bounds, config reload, and stale transitions.
- **Guided CLI:** environment-file-backed `serve`; hidden-secret `setup`; one-time exact-source `setup observe-proxy`; redacted upstream/join/health `doctor`; strict `validate` reports with labeled identity comparisons and summary/private privacy modes; configuration-free `healthcheck`; strict shared env-value decoding; and cooperating-writer directory locks.
- **Deployment:** unchanged one-service/two-network hardening, explicit Headscale env example, separate OAuth-only Tailscale env example, parameterized raw/rendered/short-include verification across all base/private-CA/service-metadata/service-health combinations; optional file overlays preserve TrueNAS `950:950`/`0750`/`0640` through supplemental numeric read groups rather than permission changes.

## Known limitations that must remain explicit

1. **One provider per process.** No multi-tailnet aggregation. Tailscale remains preview.
2. **Narrow policy subset.** Headscale remains legacy-ACL-only. Tailscale accepts additive legacy ACLs plus safe network Grants and recognized attr-only `funnel` `nodeAttrs`; authoritative role membership applies only to Grants, uses exact login, includes Owner-to-Admin automatic membership, keeps specialized roles isolated, and is never inferred from devices/tags/owners. Posture, routing, services, IP sets, applications, malformed capabilities, and unknown semantics reject refresh.
3. **Rule-kind-specific port behavior.** Legacy ACL ports/protocols remain unmodeled. Grants require TCP to the exact NPM backend port; non-TCP-only Grants produce no HTTP card.
4. **Unproven join.** NPM `ForwardHost` is the join key, but real values may be Docker names while Headscale destinations resolve to IPs/tags.
5. **NPM access lists unused.** The runtime does not fetch or use them for visibility.
6. **Identity adapters absent.** Authentik/Authelia/`Remote-User`/`X-Webauth-*` are not accepted.
7. **Host-loopback trust boundary.** Host processes or host-network containers that can reach loopback can share the Docker-gateway source identity trusted for Serve ingress.
8. **Restricted HTTP is topology-dependent.** The hostname allowlist does not prove that the real Docker bridge is private, that Headscale port `8080` is not published elsewhere, or that direct LAN routing is absent.
9. **NPM control-proxy exposure.** NPM can observe Headscale control traffic and operator Bearer API keys and becomes required for new-client enrollment and workstation operations.
10. **Headscale automatic HTTPS Serve is future work.** Track upstream issue #2527 and PR #3300; canonical tailnet HTTP Serve remains acceptable over WireGuard.
11. **Endpoint/control-plane compromise remains in scope.** WireGuard prevents ordinary on-path LAN/router/ISP interception, not compromise of clients, TrueNAS, Tailscale/Headscale control components, NPM, or trusted host workloads.
12. **Presentation metadata is explicit and narrow.** It cannot create cards, alter joins, or grant visibility. Wildcard-only cards remain unlinked until NPM has a concrete name or metadata supplies a URL.
13. **Backend health is narrow observational evidence.** It is explicit opt-in, shared, memory-only, and direct-backend. It is not NPM route state, authorization, browser-path proof, persistent history, or `/healthz` readiness.
14. **No real end-to-end validation yet.** Fixtures and local probes are not production proof.

## Next work

1. Finish sequential verification of `feature/bounded-service-health`: full race/vet, all Compose combinations, strict MkDocs, static image/CLI checks, supplemental-group readability, and an independent SSRF/DNS-rebinding/credential-isolation/authorization-boundary review.
2. Commit/push/PR only at the source-control checkpoint; keep Tailscale labeled preview and never replace a published RC.
3. Before any live deployment, present a separate TrueNAS checkpoint for the image/health overlay only. Do not change datasets, permissions, the live tailnet policy, NPM, Headscale, Tailscale, DNS, certificates, networks, or ports without separate approval.
4. For the current wildcard service, prefer adding the real concrete hostname to the same NPM proxy host; use metadata only if that is unsuitable. Avoid a duplicate NPM host/card.
5. Design privacy-minimized ephemeral hostname suggestions next, then organization/personalization and Tailscale SSH machine cards as separate tasks.
6. Complete card/reachability parity, unsupported-policy negatives, token refresh/revocation, stale/cold recovery, Serve header replacement, and LAN-negative raw-port isolation. Retain Tailscale `preview` until all live acceptance requirements pass.

## Verification discipline

Heavy verification must be run sequentially and contained because this host has previously experienced PSI pressure. Complete the full race, Compose, strict MkDocs, and image/CLI checks before release work. Publication and live TrueNAS changes remain separate approval checkpoints.

## Canonical pointers

- Public scope and roadmap: `README.md`
- TrueNAS deployment: `docs/guides/truenas-scale.md`
- Headscale + NPM architecture: `docs/guides/headscale-npm.md`
- Tailscale SaaS preview: `docs/guides/tailscale-saas-npm.md`
- Tailscale API: `docs/reference/tailscale-api.md`
- Known limitations: `docs/reference/known-limitations.md`
- Real-deployment validation: `docs/getting-started/validation.md`
- Optional native Headscale TLS: `docs/guides/private-tls.md`
- Workstation administration CLI: `../headscale-ops/README.md`
- API research: `knowledgebase/01-api-research.md`
- Locked decisions: `knowledgebase/02-design-decisions.md`
- Deep research: `knowledgebase/05-deep-research.md`
