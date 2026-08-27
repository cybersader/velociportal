# 04 — Handoff Context

> Hot context for whoever picks this up next. Stable reasoning remains in the other numbered knowledgebase documents; public operational truth lives under `docs/`.

## Current stage

**RC.1 failed the first live TrueNAS network-attachment gate; RC.2 corrects the topology defect, and task #49 adds a provider-aware control-plane layer.** One process now selects Headscale or Tailscale SaaS. Headscale remains the supported implementation path; Tailscale is an OAuth-only, fixed-origin, fixture-tested preview. The production bundle remains one service on exactly two networks. NPM always uses `velociportal-upstreams`; Headscale mode also uses its private alias, while Tailscale SaaS uses the preferred default network for verified HTTPS egress. The base bundle has no CA mount; `compose.private-ca.yaml` remains optional.

TrueNAS catalog renderer library 2.3.4 replaces an app's implicit default network whenever any UI-managed network is selected. RC.1 made the selected bridge `internal: true`, so Headscale lost outbound Docker DNS, failed its mandatory DERP-map fetch, and restart-looped. Removing the network entry restored `{"status":"pass"}`; NPM was not attached. RC.2 makes the bridge a normal user-defined network and gives Velociportal's fixed ingress bridge the preferred gateway priority. A normal bridge provides outbound NAT without publishing container ports to the LAN.

Browser ingress remains the existing host-network TrueNAS Tailscale app with declarative HTTP Serve on `:8081`, forwarding to `http://127.0.0.1:18080`. In Headscale mode, existing NPM terminates trusted HTTPS for new clients and workstation `headscale-ops`, then proxies to Headscale over the private bridge; runtime bypasses that control proxy. Tailscale SaaS mode needs neither Headscale nor this NPM control path; NPM remains service discovery.

RC.1 artifacts remain preserved as rejected acceptance evidence. RC.2 was published as `v0.1.0-rc.2` at digest `sha256:c1dc0449b4a5b411c8713c01829316e5b94ba8e90388a2a7aeac2ec433580ff9`, installed on TrueNAS, and verified with `Internal=false`, the fixed ingress network, loopback-only publication, and container hardening intact. Headscale and NPM are both attached to `velociportal-upstreams` with their exact aliases; Headscale health/DERP egress passed, and NPM listeners, DNS/HTTPS egress, LAN proxying, and existing certificate presentation passed. Velociportal remains stopped because runtime credentials are intentionally unset. The remaining Headscale bootstrap/control-proxy/policy/Serve/two-identity acceptance and all Tailscale SaaS live acceptance are still pending. The `forward_host` join remains unproven for both providers.

## Locked direction

- Visibility layer only; never authentication or enforcement.
- Single non-root `FROM scratch` container.
- One selected control plane plus NPM: Headscale policy/nodes or Tailscale OAuth/policy/users/devices, plus NPM proxy hosts.
- Shared support mode: `legacy_acl_visibility_v1`; Headscale `supported`, Tailscale `preview` until live acceptance.
- New files select `CONTROL_PLANE` explicitly; absent selection is v0.2 Headscale compatibility only and warns before v0.3 enforcement.
- Browser identity: only trusted `Tailscale-User-*` headers from Tailscale Serve.
- Canonical browser ingress: tailnet HTTP `:8081` over WireGuard to loopback `127.0.0.1:18080`.
- Canonical local runtime upstreams: NPM and, in Headscale mode, Headscale over the private egress-capable bridge and exact aliases. Tailscale SaaS uses fixed-origin verified HTTPS over the preferred default network.
- Headscale outside the local HTTP allowlist: verified HTTPS only, with no redirects, environment proxies, or insecure verification.
- Tailscale: OAuth-only fixed origin, exact four read scopes, `-` alias, in-memory tokens, early/coalesced refresh, one retry after `401`, strict owner mapping, and complete client-ID/secret/token redaction.
- Headscale-mode pre-tailnet control/API: trusted NPM HTTPS endpoint reached through split-horizon/private DNS with an existing DNS-01 wildcard certificate, no public Headscale DNS record or exact-host certificate-transparency disclosure, WebSocket/upgrade preservation, and explicit NPM trust/availability exposure.
- Separate Headscale operator and Velociportal runtime keys.
- No application database; one atomic in-memory snapshot.
- No raw Velociportal or Headscale API port on the LAN.
- Native Headscale private-CA TLS is optional only; no PKI service.
- No CA or durable app state on the router.

## Implemented behavior

- **Provider core:** neutral policy/rule/node model, whole-provider load interface, typed stages/errors, shared fail-closed policy validator, support metadata, and cross-provider matcher parity fixtures.
- **Configuration:** `CONTROL_PLANE=headscale|tailscale`; absent selector defaults to Headscale with a v0.2 deprecation warning. Only active provider keys are required. Inactive known keys are ignored with key-name-only warnings and included in redaction inputs.
- **Setup:** provider-first prompts, Headscale supported/Tailscale preview labels, selected-family-only credentials, explicit selector output, confirmed deletion of inactive known keys on switch, unknown-key preservation, and byte-identical refusal/abort behavior.
- **Headscale client:** isolated transport with TLS 1.2 minimum when TLS is used, no environment proxy, no redirects, bounded headers/bodies, Bearer auth, policy wrapper parsing, limited huJSON normalization, and node parsing including current and legacy tag fields.
- **Tailscale client:** fixed verified production origin, exact four OAuth scopes, `-` tailnet alias, in-memory token reuse/refresh/concurrency, one retry after `401`, policy/users/devices load, strict exact-login owner mapping, partial-response rejection, and credential/token redaction. Support level remains preview.
- **NPM client:** separate isolated transport with no environment proxy or redirects and bounded headers/bodies; credential JWT, proactive reauthentication, one retry on `401`, and proxy-host parsing.
- **Diagnostics:** Doctor reports explicit/implicit selection, provider-specific OAuth/API progress, policy mode, support label, and SSH separation without printing client IDs, tokens, secrets, or tailnet data. Validation schema v2 records non-sensitive provider/policy/support/selection metadata, warns on implicit Headscale, and retains the Headscale HTTP route notice.
- **Cache:** synchronous startup refresh followed by a ticker; per-stage timeouts; publish only when the complete selected-provider result and proxy hosts succeed; failed refresh keeps the exact previous complete snapshot.
- **Identity middleware:** reject sources outside `TRUSTED_PROXY_CIDR`; require `Tailscale-User-Login`; optional name/profile fields.
- **Matcher:** exact full-domain identity handling; short/bare legacy identities only when the trusted header itself is short/bare; groups, exact destinations, CIDRs, policy host aliases, destination tags, wildcard, and `autogroup:self`; one shared evidence-returning evaluator feeds portal cards and validation reports.
- **Fail-closed cases:** blank login, unsupported destination tokens/autogroups including `autogroup:internet`, and missing ACL matches render no cards. Ports and protocols are ignored rather than fail-closed.
- **No human source-tag inference:** neither `tagOwners` nor tags on owned nodes make a user a `tag:*` source.
- **Portal:** embedded server-rendered HTML, escaped card content, HTTP/HTTPS scheme allowlist, responsive light/dark theme, NPM status dots, embedded htmx refresh.
- **Guided CLI:** environment-file-backed `serve`; hidden-secret `setup`; one-time exact-source `setup observe-proxy`; redacted upstream/join `doctor`; strict `validate` reports with labeled identity comparisons and summary/private privacy modes; configuration-free `healthcheck`; strict shared env-value decoding; and cooperating-writer directory locks.
- **Deployment:** unchanged one-service/two-network hardening, explicit Headscale env example, separate OAuth-only Tailscale env example, parameterized raw/rendered/short-include/private-CA Compose verification, generalized comments, and optional private-CA public-root overlay only.

## Known limitations that must remain explicit

1. **One provider per process.** No multi-tailnet aggregation. Tailscale remains preview.
2. **Legacy `acls` only.** Grants, posture, IP sets, services, SSH card evidence, capabilities, protocols, and other policy constructs are not evaluated; unsafe non-empty access-control constructs reject refresh.
3. **Ports/protocols ignored.** Visibility matching strips destination ports and does not model protocol.
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

1. Complete sequential verification and independent review of the dual-provider implementation. Keep Tailscale labeled preview; do not publish or deploy until those checks pass and publication is separately approved.
2. Decide which provider to use for the first live Velociportal acceptance. One process selects one provider; Headscale and Tailscale SaaS acceptance remain separate paths.
3. For Tailscale SaaS, create a dedicated OAuth client only after explicit approval, then verify exact scopes, fixed origin, token refresh/revocation, owner mapping, unsupported-policy negatives, two identities, stale/cold recovery, NPM joins, Serve identity, LAN-negative ingress, and actual reachability.
4. For Headscale, configure the trusted NPM HTTPS control endpoint and WebSocket/upgrade behavior, verify it from a pre-tailnet client, perform one short-lived local bootstrap, create separate operator/runtime keys with HTTPS-only `headscale-ops`, and expire the bootstrap key.
5. Configure the selected provider's real legacy ACL policy, at least two meaningfully different identities/devices, and declarative Tailscale HTTP Serve `:8081 -> http://127.0.0.1:18080`.
6. Start Velociportal only after writing the selected provider credentials directly to the protected administrator-managed runtime file. Never place credentials in chat or acceptance records.
7. If Headscale is selected, remove host port `30210` only after both the private runtime alias and trusted HTTPS control path pass.
8. Complete the two-identity, header-replacement, LAN-negative, restart, join, card-link, and actual selected-control-plane reachability worksheet before any support claim.

## Verification discipline

Heavy verification must be run sequentially and contained because this host has previously experienced PSI pressure. Task #49 authorizes focused Go/config/deployment tests and formatting only. Do not run or claim the full race suite, image/container verification, strict MkDocs build, release verification, or live acceptance from this task.

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
