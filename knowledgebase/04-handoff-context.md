# 04 — Handoff Context

> Hot context for whoever picks this up next. Stable reasoning remains in the other numbered knowledgebase documents; public operational truth lives under `docs/`.

## Current stage

**Sprint 5 explainable validation tooling is complete in the working tree.** The project has a working Go binary, interactive hidden-secret setup, exact trusted-proxy observation, doctor diagnostics, privacy-controlled multi-identity validation reports, build provenance, a binary-native healthcheck, health-gated Compose startup, race-enabled tests, hardened identity/matcher behavior, an all-or-nothing polling snapshot, a responsive light/dark portal, branded MkDocs pages, canonical TrueNAS/VPS guides, and CI/release verification that does not publish by default.

No commit or production deployment is implied by this note. The major remaining milestone is completing the real-deployment worksheet against actual Headscale, NPM, and identity-proxy data and deciding whether the `forward_host` join is viable.

## Locked direction

- Visibility layer only; never an authentication or enforcement layer.
- Single non-root `FROM scratch` container.
- Current data sources: Headscale policy + nodes and NPM proxy hosts.
- Identity: only trusted `Tailscale-User-*` headers.
- Go 1.22 standard library, embedded server-rendered HTML, embedded htmx.
- No application database; one atomic in-memory snapshot.

## Implemented behavior

- **Headscale client:** Bearer auth; policy wrapper parsing; limited huJSON normalization; node parsing including current and legacy tag fields.
- **NPM client:** credential JWT, proactive reauthentication, one retry on `401`, proxy-host parsing.
- **Cache:** synchronous startup refresh followed by a ticker; per-upstream context timeouts; publishes only when policy, nodes, and proxy hosts all succeed; failed refresh keeps the previous complete snapshot.
- **Identity middleware:** rejects sources outside `TRUSTED_PROXY_CIDR`; requires `Tailscale-User-Login`; optional name/profile fields.
- **Matcher:** exact full-domain identity handling; short/bare legacy identities only when the trusted header itself is short/bare; groups, exact destinations, CIDRs, policy host aliases, destination tags, wildcard, and `autogroup:self`; one shared evidence-returning evaluator feeds both portal cards and validation reports.
- **Fail-closed cases:** blank login, unsupported destination tokens/autogroups including `autogroup:internet`, and missing ACL matches render no cards. Ports and protocols are a separate limitation: they are ignored rather than fail-closed.
- **No human source-tag inference:** neither `tagOwners` nor tags on owned nodes make a user a `tag:*` source.
- **Portal:** embedded server-rendered HTML, escaped card content, HTTP/HTTPS scheme allowlist, responsive light/dark theme, NPM status dots, embedded htmx refresh.
- **Guided CLI:** environment-file-backed `serve`; hidden-secret `setup`; one-time exact-source `setup observe-proxy`; redacted upstream/join `doctor`; strict `validate` reports with labeled identity comparisons and summary/private privacy modes; configuration-free `healthcheck`; strict shared env-value decoding; and cooperating-writer directory locks around setup/observer updates.
- **Deployment:** non-root scratch image, read-only Compose container, loopback-only host publication, binary-native Docker healthcheck, a stable Compose gateway, and project-relative Make wrappers for setup through health-gated startup.
- **Docs/CI:** branded Material pages and accessible Mermaid diagrams, strict MkDocs validation, Compose and in-image CLI validation, canonical public deployment guides, known-limitations page, CI-gated/cancelable documentation deployment, and a serialized manual-only release workflow that can repair one missing immutable tag from the other verified tag.

## Current test coverage

The race-enabled suite covers:

- Headscale and NPM client request/response flows with `httptest`.
- Identity trusted/untrusted/missing behavior.
- Config defaults, bounded poll intervals, strict raw-Compose value decoding, and rejection of whole-family trusted CIDRs including `::ffff:0:0/96`.
- Cache startup, replacement, skipped unused endpoints, and all-or-nothing failure behavior.
- Full-domain identity separation, blank login fail-closed behavior, source-tag non-inference, destination tags, CIDRs, hosts, `autogroup:self`, and `autogroup:internet` fail-closed behavior.
- Portal filtering, escaping, scheme allowlist, empty states, full-domain identities, light/dark CSS, status dots, and htmx markup.
- CLI dispatch and exit codes; deterministic env-file parsing/serialization; atomic owner-only writes; cooperating-writer locking and concurrent-change rejection; hidden-secret redaction; proxy observer spoof resistance and exact CIDRs; doctor upstream failures and identity previews; deterministic validation JSON, privacy canaries, identity comparisons, join evidence, and build provenance; healthcheck status, redirect, timeout, and size bounds.

Do not reduce this to a stale fixed test count; subtests and future additions make the exact number volatile.

## Known limitations that must remain explicit

1. **Legacy `acls` only.** Grants, SSH, posture, capabilities, protocols, and other policy constructs are not evaluated.
2. **Ports/protocols ignored.** Visibility matching strips destination ports and does not model protocol.
3. **Unproven join.** NPM `ForwardHost` is the join key, but real NPM values may be Docker names while Headscale destinations resolve to IPs/tags.
4. **NPM access lists unused.** The runtime does not fetch or use them for visibility.
5. **Identity adapters absent.** Authentik/Authelia/`Remote-User`/`X-Webauth-*` are not accepted.
6. **Host-loopback trust boundary.** In the repository topology, trusted host-proxy traffic appears as the Docker gateway, so other local host processes can share that source identity; use a dedicated proxy-to-app network when the host runs untrusted workloads.
7. **Headscale HTTPS Serve gap.** Native automatic HTTPS Serve depends on Tailscale's certificate flow and is not currently implemented by Headscale; public docs describe tailnet-only HTTP or an external identity-aware HTTPS proxy.
8. **No real end-to-end validation yet.** Fixtures are not a production proof.

## Next work

1. Tie the tested candidate to a clean source revision or immutable image digest.
2. Run `make validate` with two opaque labels for real identities with different groups.
3. Complete `docs/getting-started/validation.md` through the actual identity proxy, including `401`/`403`, header stripping, links, and reachability.
4. Classify every enabled NPM `forward_host` against the report's supported selector evidence.
5. Decide whether to retain `forward_host` or design an evidence-backed explicit mapping; only then create manually reviewed sanitized fixtures if needed.
6. Design safe port/protocol semantics before adding them.
7. Decide whether Grants support belongs in the same matcher or a separate policy evaluator.
8. Only after the primary deployment is proven, consider Tailscale SaaS, Caddy, Traefik, or IdP header adapters.

## Canonical pointers

- Public scope and roadmap: `README.md`
- Known limitations: `docs/reference/known-limitations.md`
- Real-deployment validation: `docs/getting-started/validation.md`
- TrueNAS deployment: `docs/guides/truenas-scale.md`
- VPS options: `docs/guides/vps-headscale.md`
- API research: `knowledgebase/01-api-research.md`
- Locked decisions: `knowledgebase/02-design-decisions.md`
- Deep research: `knowledgebase/05-deep-research.md`
