# 04 — Handoff Context

> Hot context for whoever picks this up next. Stable reasoning remains in the other numbered knowledgebase documents; public operational truth lives under `docs/`.

## Current stage

**Task 31 architecture is implemented in the working tree; live acceptance is pending.** Velociportal now accepts Headscale HTTP only for an exact local/internal allowlist and otherwise requires verified HTTPS. The production bundle creates `velociportal-upstreams`; canonical runtime URLs are `http://headscale.velociportal.internal:8080` and `http://npm.velociportal.internal:81`. The base bundle has no CA mount; `compose.private-ca.yaml` remains an optional verified-HTTPS overlay.

Browser ingress is the existing host-network TrueNAS Tailscale app with declarative HTTP Serve on `:8081`, forwarding to `http://127.0.0.1:18080`. Existing NPM terminates the trusted HTTPS Headscale endpoint used by new clients and workstation `headscale-ops`, then proxies to Headscale over the internal network. Runtime Velociportal bypasses NPM. `headscale-ops` remains workstation-only and HTTPS-only.

No commit, published artifact, production deployment, or public support claim is implied. The one-time API-key bootstrap, separate operator/runtime keys, NPM control-proxy acceptance, node/policy setup, declarative Serve verification, two-identity card comparison, LAN-negative tests, and real Headscale reachability checks are still pending. The `forward_host` join remains unproven.

## Locked direction

- Visibility layer only; never authentication or enforcement.
- Single non-root `FROM scratch` container.
- Current inputs: Headscale policy + nodes and NPM proxy hosts.
- Browser identity: only trusted `Tailscale-User-*` headers from Tailscale Serve.
- Canonical browser ingress: tailnet HTTP `:8081` over WireGuard to loopback `127.0.0.1:18080`.
- Canonical runtime upstreams: direct HTTP over `velociportal-upstreams` and exact Docker aliases.
- Headscale outside the local HTTP allowlist: verified HTTPS only, with no redirects, environment proxies, or insecure verification.
- Pre-tailnet Headscale control/API: trusted NPM HTTPS endpoint reached through split-horizon/private DNS with an existing DNS-01 wildcard certificate, no public Headscale DNS record or exact-host certificate-transparency disclosure, WebSocket/upgrade preservation, and explicit NPM trust/availability exposure.
- Separate Headscale operator and Velociportal runtime keys.
- No application database; one atomic in-memory snapshot.
- No raw Velociportal or Headscale API port on the LAN.
- Native Headscale private-CA TLS is optional only; no PKI service.
- No CA or durable app state on the router.

## Implemented behavior

- **Configuration:** HTTPS is accepted generally; HTTP Headscale URLs are accepted only for exact allowlisted local/internal host forms. Disallowed HTTP hosts fail startup.
- **Headscale client:** isolated transport with TLS 1.2 minimum when TLS is used, no environment proxy, no redirects, bounded headers/bodies, Bearer auth, policy wrapper parsing, limited huJSON normalization, and node parsing including current and legacy tag fields.
- **NPM client:** separate isolated transport with no environment proxy or redirects and bounded headers/bodies; credential JWT, proactive reauthentication, one retry on `401`, and proxy-host parsing.
- **Diagnostics:** setup, doctor, and validation explicitly warn that accepted Headscale HTTP does not itself prove route confinement or external inaccessibility. Validation records a non-failing notice without exposing the configured route or credentials.
- **Cache:** synchronous startup refresh followed by a ticker; per-upstream timeouts; publish only when policy, nodes, and proxy hosts all succeed; failed refresh keeps the previous complete snapshot.
- **Identity middleware:** reject sources outside `TRUSTED_PROXY_CIDR`; require `Tailscale-User-Login`; optional name/profile fields.
- **Matcher:** exact full-domain identity handling; short/bare legacy identities only when the trusted header itself is short/bare; groups, exact destinations, CIDRs, policy host aliases, destination tags, wildcard, and `autogroup:self`; one shared evidence-returning evaluator feeds portal cards and validation reports.
- **Fail-closed cases:** blank login, unsupported destination tokens/autogroups including `autogroup:internet`, and missing ACL matches render no cards. Ports and protocols are ignored rather than fail-closed.
- **No human source-tag inference:** neither `tagOwners` nor tags on owned nodes make a user a `tag:*` source.
- **Portal:** embedded server-rendered HTML, escaped card content, HTTP/HTTPS scheme allowlist, responsive light/dark theme, NPM status dots, embedded htmx refresh.
- **Guided CLI:** environment-file-backed `serve`; hidden-secret `setup`; one-time exact-source `setup observe-proxy`; redacted upstream/join `doctor`; strict `validate` reports with labeled identity comparisons and summary/private privacy modes; configuration-free `healthcheck`; strict shared env-value decoding; and cooperating-writer directory locks.
- **Deployment:** non-root scratch image, read-only container, loopback-only host publication, binary-native Docker healthcheck, stable ingress bridge, named internal upstream network, exact Headscale/NPM aliases, and optional private-CA public-root overlay only.

## Known limitations that must remain explicit

1. **Legacy `acls` only.** Grants, SSH, posture, capabilities, protocols, and other policy constructs are not evaluated.
2. **Ports/protocols ignored.** Visibility matching strips destination ports and does not model protocol.
3. **Unproven join.** NPM `ForwardHost` is the join key, but real values may be Docker names while Headscale destinations resolve to IPs/tags.
4. **NPM access lists unused.** The runtime does not fetch or use them for visibility.
5. **Identity adapters absent.** Authentik/Authelia/`Remote-User`/`X-Webauth-*` are not accepted.
6. **Host-loopback trust boundary.** Host processes or host-network containers that can reach loopback can share the Docker-gateway source identity trusted for Serve ingress.
7. **Restricted HTTP is topology-dependent.** The hostname allowlist does not prove that the real Docker network is private or that Headscale port `8080` is not published elsewhere.
8. **NPM control-proxy exposure.** NPM can observe Headscale control traffic and operator Bearer API keys and becomes required for new-client enrollment and workstation operations.
9. **Headscale automatic HTTPS Serve is future work.** Track upstream issue #2527 and PR #3300; canonical tailnet HTTP Serve remains acceptable over WireGuard.
10. **Endpoint/control-plane compromise remains in scope.** WireGuard prevents ordinary on-path LAN/router/ISP interception, not compromise of clients, TrueNAS, Tailscale/Headscale control components, NPM, or trusted host workloads.
11. **No real end-to-end validation yet.** Fixtures are not production proof.

## Next work

1. Back up TrueNAS app settings, Headscale data/policy, and NPM database/configuration/certificate state.
2. Import the production Compose definition so `velociportal-upstreams` exists, then attach Headscale and NPM through TrueNAS UI network settings with the exact aliases.
3. Set Headscale port `8080` to None/Expose only, configure its external `server_url` to the trusted NPM HTTPS hostname, and configure the NPM proxy with WebSocket/upgrade support and the existing automated trusted certificate.
4. Verify that endpoint from a pre-tailnet client. If it is not trusted, stop; do not disable verification.
5. Perform exactly one short-lived local API-key bootstrap, then use HTTPS-only `headscale-ops` to create separate operator and Velociportal runtime keys and expire the bootstrap key.
6. Configure the real legacy ACL policy and at least two meaningfully different users/nodes.
7. Configure declarative Tailscale HTTP Serve `:8081 -> http://127.0.0.1:18080` through the TrueNAS Tailscale app UI.
8. Deploy Velociportal with the direct internal upstream URLs and dedicated runtime key.
9. Complete the two-identity, header-replacement, LAN-negative, restart, join, card-link, and actual Headscale reachability worksheet.
10. Only after that evidence exists, decide whether `forward_host` is retained and whether a public support claim is justified.

## Verification discipline

Heavy verification must be run sequentially and contained because this host has previously experienced PSI pressure. For this task only lightweight documentation hygiene was authorized. Do not claim Go, Compose, MkDocs, container, or live acceptance results unless those checks are separately run and recorded.

## Canonical pointers

- Public scope and roadmap: `README.md`
- TrueNAS deployment: `docs/guides/truenas-scale.md`
- Headscale + NPM architecture: `docs/guides/headscale-npm.md`
- Known limitations: `docs/reference/known-limitations.md`
- Real-deployment validation: `docs/getting-started/validation.md`
- Optional native Headscale TLS: `docs/guides/private-tls.md`
- Workstation administration CLI: `../headscale-ops/README.md`
- API research: `knowledgebase/01-api-research.md`
- Locked decisions: `knowledgebase/02-design-decisions.md`
- Deep research: `knowledgebase/05-deep-research.md`
