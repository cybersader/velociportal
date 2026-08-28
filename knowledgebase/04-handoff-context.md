# 04 — Handoff Context

> Hot context for whoever picks this up next. Stable reasoning remains in the other numbered knowledgebase documents; public operational truth lives under `docs/`.

## Current stage

**Published `v0.2.0-rc.2` is immutable at `ghcr.io/cybersader/velociportal@sha256:bf1b1324aa5cfd1a7f744d566546440543177b845c7d184a0318773632046019` and resolves to commit `159a9242b61f3f14d1d9ac4d6833e6fc80c4ed56`.** It contains the safe-Grants correction. Live acceptance proved OAuth/policy/users/devices connectivity, NPM startup, a complete healthy snapshot, declarative Serve ingress, and supported destination/TCP matching. The working tailnet policy was not changed.

That acceptance exposed the remaining source-side gap: the real human rule uses `autogroup:admin`, but RC.2 did not retain authoritative per-`loginName` Users API role membership, so expected role-derived cards were omitted. Branch `fix/tailscale-role-grants` implements exact Users API `loginName`/`type`/`role` validation, Grant-only role membership, and separate device-owner mapping. Direct members receive `autogroup:member` plus exactly their API role; shared users receive none; there is no hierarchy or device/tag/owner inference. Focused tests, the ordinary suite, vet, race tests, Compose verification, image smoke checks, and an independent read-only review pass; strict documentation verification remains before the source-control checkpoint.

The production topology remains one service on exactly two networks. NPM always uses the egress-capable `velociportal-upstreams`; Headscale mode also uses its private alias, while Tailscale SaaS uses the preferred default network for fixed-origin verified HTTPS egress. Browser ingress remains declarative Tailscale HTTP Serve on `:8081` to `http://127.0.0.1:18080`. The base bundle has no CA mount; `compose.private-ca.yaml` remains optional.

`v0.2.0-rc.3` is the next preview candidate. Never replace or retag RC.2. Publication and the next TrueNAS app update require separate explicit approval. The live policy must not be rewritten. Two-identity role-card/reachability parity, token refresh/revocation, unsupported-policy negatives, stale/cold recovery, header replacement, and LAN-negative isolation remain pending.

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
- **Tailscale client:** fixed verified production origin, exact four OAuth scopes, `-` tailnet alias, in-memory token reuse/refresh/concurrency, one retry after `401`, policy/users/devices load, strict canonical user `type`/`role` validation, exact-login Grant-role selector construction, separate strict device-owner mapping, partial-response rejection, and credential/token redaction. Support level remains preview.
- **NPM client:** separate isolated transport with no environment proxy or redirects and bounded headers/bodies; credential JWT, proactive reauthentication, one retry on `401`, and proxy-host parsing.
- **Diagnostics:** Doctor reports explicit/implicit selection, provider-specific OAuth/API progress, access-rule counts, policy mode, support label, and SSH separation without printing client IDs, tokens, secrets, or tailnet data. Validation schema v3 records non-sensitive provider/policy/support/selection metadata plus `access_rules`, `rule_kind`, and original `rule_index`; it warns on implicit Headscale and retains the Headscale HTTP route notice.
- **Cache:** synchronous startup refresh followed by a ticker; per-stage timeouts; publish only when the complete selected-provider result and proxy hosts succeed; failed refresh keeps the exact previous complete snapshot.
- **Identity middleware:** reject sources outside `TRUSTED_PROXY_CIDR`; require `Tailscale-User-Login`; optional name/profile fields.
- **Matcher:** exact full-domain identity handling; short/bare legacy identities only when the trusted header itself is short/bare; groups, exact destinations, CIDRs, policy host aliases, destination tags, wildcard, and `autogroup:self`; authoritative role selectors are consulted only for Grant rules and only by exact login; Grant cards require TCP to the exact NPM backend port; one shared evidence-returning evaluator feeds portal cards and validation reports.
- **Fail-closed cases:** blank login, unsupported selectors/autogroups including `autogroup:internet`, posture, routing, services, IP sets, application capabilities, malformed capabilities, unknown semantics, and missing supported access-rule matches render no cards or reject the complete refresh as appropriate. Legacy ACL ports/protocols remain unmodeled; valid non-TCP Grants produce no HTTP card.
- **Authoritative roles without machine inference:** supported human-role autogroups map to a browser identity only through exact Users API membership and only for Grants. Machine/tag/IP/CIDR/host selectors, `autogroup:shared`, `autogroup:tagged`, and other machine autogroups may load but never become human; neither device ownership, `tagOwners`, nor tags on owned nodes confer source membership. Only recognized attr-only `funnel` `nodeAttrs` load, and they never authorize.
- **Portal:** embedded server-rendered HTML, escaped card content, HTTP/HTTPS scheme allowlist, responsive light/dark theme, NPM status dots, embedded htmx refresh.
- **Guided CLI:** environment-file-backed `serve`; hidden-secret `setup`; one-time exact-source `setup observe-proxy`; redacted upstream/join `doctor`; strict `validate` reports with labeled identity comparisons and summary/private privacy modes; configuration-free `healthcheck`; strict shared env-value decoding; and cooperating-writer directory locks.
- **Deployment:** unchanged one-service/two-network hardening, explicit Headscale env example, separate OAuth-only Tailscale env example, parameterized raw/rendered/short-include/private-CA Compose verification, generalized comments, and optional private-CA public-root overlay only.

## Known limitations that must remain explicit

1. **One provider per process.** No multi-tailnet aggregation. Tailscale remains preview.
2. **Narrow policy subset.** Headscale remains legacy-ACL-only. Tailscale accepts additive legacy ACLs plus safe network Grants and recognized attr-only `funnel` `nodeAttrs`; authoritative role membership applies only to Grants, uses exact login, has no hierarchy, and is never inferred from devices/tags/owners. Posture, routing, services, IP sets, applications, malformed capabilities, and unknown semantics reject refresh.
3. **Rule-kind-specific port behavior.** Legacy ACL ports/protocols remain unmodeled. Grants require TCP to the exact NPM backend port; non-TCP-only Grants produce no HTTP card.
4. **Unproven join.** NPM `ForwardHost` is the join key, but real values may be Docker names while Headscale destinations resolve to IPs/tags.
5. **NPM access lists unused.** The runtime does not fetch or use them for visibility.
6. **Identity adapters absent.** Authentik/Authelia/`Remote-User`/`X-Webauth-*` are not accepted.
7. **Host-loopback trust boundary.** Host processes or host-network containers that can reach loopback can share the Docker-gateway source identity trusted for Serve ingress.
8. **Restricted HTTP is topology-dependent.** The hostname allowlist does not prove that the real Docker bridge is private, that Headscale port `8080` is not published elsewhere, or that direct LAN routing is absent.
9. **NPM control-proxy exposure.** NPM can observe Headscale control traffic and operator Bearer API keys and becomes required for new-client enrollment and workstation operations.
10. **Headscale automatic HTTPS Serve is future work.** Track upstream issue #2527 and PR #3300; canonical tailnet HTTP Serve remains acceptable over WireGuard.
11. **Endpoint/control-plane compromise remains in scope.** WireGuard prevents ordinary on-path LAN/router/ISP interception, not compromise of clients, TrueNAS, Tailscale/Headscale control components, NPM, or trusted host workloads.
12. **No real end-to-end validation yet.** Fixtures are not production proof.

## Next work

1. Finish documentation and sequential verification of `fix/tailscale-role-grants`, including direct-member/shared-user semantics, no hierarchy, exact-login matching, Grant-only isolation, strict user-field failures, and private/summary validation evidence.
2. Commit/push/PR only at the source-control checkpoint; keep Tailscale labeled preview.
3. Publish a new immutable `v0.2.0-rc.3` only after separate explicit approval; never replace or retag RC.2.
4. Restage the protected TrueNAS bundle and update only Velociportal after a fresh TrueNAS change checkpoint. Do not change datasets, permissions, the live tailnet policy, NPM, Headscale, Tailscale, DNS, certificates, networks, or ports.
5. Resume Tailscale live acceptance with the real policy: confirm the same Admin identity now receives `autogroup:admin`-derived cards, an ordinary member does not, shared/machine sources remain non-human, and two real identities differ as intended.
6. Complete card/reachability parity, unsupported-policy negatives, token refresh/revocation, stale/cold recovery, Serve header replacement, and LAN-negative raw-port isolation.
7. Retain Tailscale `preview` until all live acceptance requirements pass. Headscale bootstrap/control-proxy/two-identity acceptance remains a separate path.

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
