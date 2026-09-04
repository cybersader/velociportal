# 04 — Handoff Context

> Hot context for whoever picks this up next. Stable reasoning remains in the other numbered knowledgebase documents; public operational truth lives under `docs/`.

## Current stage

**Published `v0.2.0-rc.8` is immutable at `ghcr.io/cybersader/velociportal@sha256:d3423da915931f4f61b65ecbdfc94094407c0d34b16b2822b1798cec9563d9e5`.** Live TrueNAS environment files reference RC.8, and live testing reached its username-triggered account settings and role-gated browser SSH action; the exact running-container digest has not been independently captured. That test exposed a full-canonical-name console query that returned zero machines plus unclear account/action wording on machine cards.

Merged main includes a one-shot private `suggest-hostnames` operator command. It combines selected-control-plane node/device names with optional bounded hostname-only stdin, rejects whole ambiguous candidate/NPM graph components, and emits only a confirmed strict metadata-v1 proposal. It adds no DNS/log scan, runtime store, portal route, active-metadata mutation, authorization evidence, production mount, or `/healthz` effect.

The current implementation also accepts strict service-metadata v2 category/order fields, renders accessible deterministic category sections with uncategorized services last, and replaces the former standalone logo toggle with a username-triggered accessible settings panel holding a per-identity browser-local show/hide preference for the fixed built-in logo (opaque SHA-256-scoped storage key, one-time legacy-key migration, optional strict `PORTAL_LOGO_DEFAULT=visible|hidden` deployment default). Version 1 parsing and hostname-proposal bytes remain compatible. Organization stays presentation-only; arbitrary per-service logos, access history, and server-side/account-synchronized personalization remain deferred.

Merged main and RC.8 include a role-gated browser SSH console action on eligible Tailscale machine cards: a direct member holding an automatic-admin-equivalent role (Owner, Admin, IT admin, or Network admin) sees a link that opens the fixed filtered `https://console.tailscale.com/admin/machines?q=<value>` page in a new tab, using a target that passes the same narrow validation as the copy commands. It is role-gated navigation only, never a direct session, proxy, enforcement check, or reachability claim — Tailscale remains responsible for console eligibility, reauthentication, SSH policy, device posture, account choice, and the session itself. It is hidden for ineligible roles, Headscale, and an unavailable Machines projection, and it adds no new endpoint, scope, credential, or tailnet setting. `MachineCard` and all SSH/Grant evidence are unchanged.

A follow-up correction on top of that feature branch fixed four UX problems without touching `MachineCard`, matching/evidence, or canonical CLI targets: (1) `machineConsoleURL` now searches Tailscale's console on the already validated short machine name (`q=server`, derived by `machineShortName` as the first label of a canonical `*.ts.net` target) instead of the full canonical name, because Tailscale's own Machines search matches short names; (2) each card shows that same short name prominently with the full canonical target underneath, via `machineCardNames`; (3) raw `accept`/`check` tokens became truthful plain-language labels (`machineActionLabel`/`machineCheckPeriodLabel`): "No extra sign-in" for `accept`, "Reauthenticate every `<period>`" for `check`, defaulting to Tailscale's documented 12 hours when `checkPeriod` is absent; (4) where `autogroup:nonroot` applies, a card now offers a safe, purely client-side, client-validated custom-account text input (never pre-filled, never sent to or verified server-side, rejecting `root`) so a viewer can copy a command for a policy-permitted non-root account Velociportal cannot enumerate — plus a browser-local, per-identity, 10-entry MRU suggestion list for that input, reusing the existing opaque SHA-256 login scope and `localStorage`, with a clear control in the settings panel. See D20/D22/D23 in `02-design-decisions.md` and the updated SSH Machines preview bullets in `docs/reference/known-limitations.md`.

The post-RC.5 SSH Machines slice adds a separate Tailscale-preview Machines section without changing HTTP service matching. Projection availability now requires both the Tailscale provider and a present, fully supported SSH policy. Headscale, absent SSH, and unsupported SSH omit the complete section and suppress per-identity Doctor machine previews; Doctor retains the top-level unsupported-policy diagnostic. A supported projection with zero identity matches preserves the portal's explicit empty Machines state. A machine requires a supported bounded SSH rule plus independent Grant TCP/22 evidence for the same exact direct member and device. Legacy ACL port 22, NPM, metadata, organization, and health cannot create machines. Cards are non-service-link policy summaries; copy commands exist only for validated literal accounts and canonical full `*.ts.net` or validated Tailscale-address targets. Projection unavailability preserves otherwise valid HTTP cards. Padded Users API `loginName` values now reject the complete refresh instead of being trimmed into exact role membership.

The production topology remains one service on exactly two networks. NPM always uses the egress-capable `velociportal-upstreams`; Headscale mode also uses its private alias, while Tailscale SaaS uses the preferred default network for fixed-origin verified HTTPS egress. Browser ingress remains declarative Tailscale HTTP Serve on `:8081` to `http://127.0.0.1:18080`. The base bundle has no host mount; `compose.private-ca.yaml`, `compose.service-metadata.yaml`, and `compose.service-health.yaml` are independent optional overlays.

RC.8 and every earlier published candidate remain immutable and must never be replaced or retagged. The current correction branch makes no TrueNAS, NPM, Tailscale, DNS, dataset, permission, network, port, policy, or active-metadata changes. Routine low-risk publication is authorized after verification, but any live proposal/metadata/app change remains a separate operational checkpoint. Two-identity service/machine and HTTP/SSH reachability parity, copied-target checks, token refresh/revocation, unsupported HTTP-policy/SSH-suppression negatives, stale/cold recovery, header replacement, LAN-negative isolation, and live hostname-proposal acceptance remain pending.

## Current diff verification handoff

The final compatibility repair centralizes machine-projection availability across matching, portal rendering, and Doctor previews. It omits the complete Machines section and per-identity Doctor machine previews for Headscale, absent SSH, and unsupported SSH; preserves the portal empty state for a supported Tailscale SSH projection with zero matches; and preserves Doctor's top-level unsupported-policy diagnostic plus all supported HTTP service previews.

Focused verification already passed, sequentially:

```bash
podman run --rm --userns=keep-id -v '/home/cybersader/01 Projects/velociportal:/src:Z' -w /src docker.io/library/golang:1.22.12 go test -count=1 -run '^(TestEvaluateMachinesRequiresSeparateSSHandGrantTCP22Evidence|TestPortalHandler_MachinesRenderAsAccessibleNonLinkablePolicyCards|TestPortalHandler_MachineSectionTracksProjectionAvailability|TestReportDoctorIdentityPreviewsKeepsMachineTopologyPrivate|TestReportDoctorIdentityPreviewsSuppressesUnavailableMachineProjection)$' .
podman run --rm --userns=keep-id -v '/home/cybersader/01 Projects/velociportal:/src:Z' -w /src docker.io/library/golang:1.22.12 go test -count=1 -run '^(TestPortal|TestRenderServiceHealthStatus|TestMachineSSHCommand|TestRunDoctor|TestReportDoctor|TestDoctor)' .
git -C '/home/cybersader/01 Projects/velociportal' diff --check
```

No repository-wide race suite, strict documentation build, production Compose matrix, image build, CLI smoke test, or live/integration check has run for this combined working tree. Run the remaining heavy verification one command at a time:

```bash
podman run --rm --userns=keep-id -v '/home/cybersader/01 Projects/velociportal:/src:Z' -w /src docker.io/library/golang:1.22.12 go vet ./...
podman run --rm --userns=keep-id -v '/home/cybersader/01 Projects/velociportal:/src:Z' -w /src docker.io/library/golang:1.22.12 go test -v -race -count=1 ./...
ENV_FILE=.env.example docker compose -f docker-compose.yml --profile tools config --quiet
python3 scripts/verify-production-compose.py
python3 -m pip install --requirement docs/requirements.txt
mkdocs build --strict
docker build --build-arg BUILD_VERSION=verify --build-arg GIT_REVISION="$(git rev-parse HEAD)" --build-arg GIT_SOURCE_STATE=dirty -t velociportal:verify .
test "$(docker image inspect --format '{{json .Config.Healthcheck.Test}}' velociportal:verify)" = '["CMD","/velociportal","healthcheck"]'
test "$(docker image inspect --format '{{.Config.User}}' velociportal:verify)" = '65534:65534'
docker run --rm --read-only --security-opt no-new-privileges velociportal:verify healthcheck --help
docker run --rm --read-only --security-opt no-new-privileges velociportal:verify validate --help
docker run --rm --read-only --security-opt no-new-privileges velociportal:verify suggest-hostnames --help
```

A final diff scan found no Compose topology, network, host-port, bind-mount, provider-origin, OAuth-scope, or credential-family change. No Compose/environment/workflow file changed. The only `deploy/` changes are documentation and the existing optional service-metadata example advancing to strict version 2 category/order fields. The one-service/two-network contract, loopback-only `127.0.0.1:18080:8080` publication, Tailscale Serve `:8081` route, `velociportal-upstreams`, exact private aliases, mount-free base stack, optional overlays, and exact four Tailscale OAuth scopes remain unchanged.

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
- Tailscale: OAuth-only fixed origin, exact four read scopes, `-` alias, in-memory tokens, early/coalesced refresh, one retry after `401`, strict exact canonical login user type/role mapping with padded-login rejection, separate strict device-owner mapping, and complete client-ID/secret/token redaction.
- SSH Machines: separate Tailscale-preview visibility only; require Tailscale plus a present supported SSH policy before rendering or previewing, then require matching SSH plus Grant TCP/22 for the same exact direct member/device. Headscale, absent SSH, and unsupported SSH omit the complete view; cards and copied commands never become enforcement/reachability proof.
- Browser SSH console action: role-gated navigation only, rendered solely for a direct member with an automatic-admin-equivalent role (Owner, Admin, IT admin, Network admin) on an eligible machine card; fixed `console.tailscale.com` URL and validated target only, never a session/proxy/reachability claim.
- Appearance preferences: per-identity, browser-local, opaque-SHA-256-scoped `localStorage` only, never sent to or stored by Velociportal; `PORTAL_LOGO_DEFAULT` supplies only the initial default when no browser preference exists.
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
- **Tailscale client:** fixed verified production origin, exact four OAuth scopes, `-` tailnet alias, in-memory token reuse/refresh/concurrency, one retry after `401`, policy/users/devices load, strict canonical user login/`type`/`role` validation with padded-login rejection, exact-login Grant/SSH-machine role selector construction including Owner-to-Admin automatic membership, separate strict device-owner mapping, partial-response rejection, and credential/token redaction. Support level remains preview.
- **NPM client:** separate isolated transport with no environment proxy or redirects and bounded headers/bodies; credential JWT, proactive reauthentication, one retry on `401`, and proxy-host parsing.
- **Diagnostics:** Doctor reports explicit/implicit selection, provider-specific OAuth/API progress, access-rule counts, policy mode, support label, and coarse SSH state/reason/rule counts. It emits per-identity machine counts only when Tailscale SSH projection availability is supported; Headscale, absent SSH, and unsupported SSH suppress those previews while retaining top-level unsupported-policy diagnostics. Health-config state and one bounded health cycle remain available without printing client IDs, tokens, secrets, machine topology/accounts/commands, health paths/hosts/IPs, raw network errors, or tailnet data. Validation schema v3 remains HTTP-service-only and records non-sensitive provider/policy/support/selection metadata plus `access_rules`, `rule_kind`, and original `rule_index`; it warns on implicit Headscale and retains the Headscale HTTP route notice.
- **Cache:** synchronous startup refresh followed by a ticker; per-stage timeouts; optional strict service metadata v1/v2 loads before upstream contact; publish only when metadata plus the complete selected-provider result and proxy hosts succeed; failed refresh keeps the exact previous complete snapshot. Health uses a separate scheduler and atomic result map and cannot change cache publication/freshness or `/healthz`.
- **Identity middleware:** reject sources outside `TRUSTED_PROXY_CIDR`; require `Tailscale-User-Login`; optional name/profile fields.
- **Service matcher:** exact full-domain identity handling; short/bare legacy identities only when the trusted header itself is short/bare; groups, exact destinations, CIDRs, policy host aliases, destination tags, wildcard, and `autogroup:self`; authoritative role selectors are consulted only for Grant rules and only by exact login; Grant cards require TCP to the exact NPM backend port; one shared evidence-returning evaluator feeds portal service cards and validation reports.
- **Machine matcher:** one shared projection-availability predicate requires Tailscale plus supported SSH before matching, portal rendering, or Doctor identity previews. Available projections use direct members only; separate exact-login/group/role matching across the supported SSH section plus independent Grant TCP/22 evidence to the same validated device address; tags/`autogroup:self` destinations; restrictive `check` precedence; shared/case/short/bare/machine-source/Headscale negatives; no NPM, metadata, organization, health, or legacy-ACL inputs; canonical full `*.ts.net` target or narrow Tailscale-address fallback.
- **Fail-closed cases:** blank login, unsupported selectors/autogroups including `autogroup:internet`, posture, routing, services, IP sets, application capabilities, malformed capabilities, unknown semantics, and missing supported access-rule matches render no cards or reject the complete refresh as appropriate. Legacy ACL ports/protocols remain unmodeled; valid non-TCP Grants produce no HTTP card.
- **Authoritative roles without machine inference:** supported human-role autogroups map to a browser identity only through exact canonical Users API membership and only for Grants/SSH Machines. Machine/tag/IP/CIDR/host selectors, `autogroup:shared`, `autogroup:tagged`, and other machine autogroups may load but never become human; neither device ownership, `tagOwners`, nor tags on owned nodes confer source membership. Only recognized attr-only `funnel` `nodeAttrs` load, and they never authorize.
- **Portal:** embedded server-rendered HTML, escaped service/machine/category content, HTTP/HTTPS scheme allowlist, first-concrete-NPM-domain selection, visible non-linkable wildcard cards, exact-ID display name/URL/category/order metadata, deterministic accessible category sections with uncategorized last, and a separate non-service-link Machines section only for an available Tailscale SSH projection. Headscale, absent SSH, and unsupported SSH omit that complete section; supported zero-match identities retain the explicit empty state. An eligible machine card additionally renders a role-gated browser SSH console link to a fixed filtered `console.tailscale.com` page. Copy controls remain limited to validated literal SSH accounts/targets, with a username-triggered accessible settings panel holding a per-identity opaque-SHA-256-scoped `localStorage` fixed-logo visibility preference (legacy-key migration, optional `PORTAL_LOGO_DEFAULT` deployment default), no NPM-derived health dots, identity-filtered accessible coarse service-health labels, responsive light/dark theme, and complete-element htmx refresh across services/machines and flat/grouped transitions.
- **Service health:** strict version-1 explicit target file; HTTP GET/TCP connect-only probes derived only from current NPM backends; exact host/suffix plus all-answer CIDR validation; direct validated-IP dialing with Host/SNI preservation; hard-denied address classes and protected API sockets; no credentials, proxies, redirects, payload inspection, or persistence; fixed workers, non-overlap, timeout/cycle bounds, config reload, and stale transitions.
- **Guided CLI:** environment-file-backed `serve`; hidden-secret `setup`; one-time exact-source `setup observe-proxy`; redacted upstream/join/health `doctor`; strict `validate` reports; one-shot private `suggest-hostnames` with bounded provider/stdin candidates, graph ambiguity rejection, literal confirmation, and no-clobber `0600` proposal output; configuration-free `healthcheck`; strict shared env-value decoding; and cooperating-writer directory locks.
- **Deployment:** unchanged one-service/two-network hardening, explicit Headscale env example, separate OAuth-only Tailscale env example, parameterized raw/rendered/short-include verification across all base/private-CA/service-metadata/service-health combinations; optional file overlays preserve TrueNAS `950:950`/`0750`/`0640` through supplemental numeric read groups rather than permission changes.

## Known limitations that must remain explicit

1. **One provider per process.** No multi-tailnet aggregation. Tailscale remains preview.
2. **Narrow policy subset.** Headscale remains legacy-ACL-only. Tailscale accepts additive legacy ACLs plus safe network Grants, recognized attr-only `funnel` `nodeAttrs`, and a separate bounded SSH projection; authoritative role membership applies only to Grants/SSH Machines, uses exact canonical login, includes Owner-to-Admin automatic membership, keeps specialized roles isolated, and is never inferred from devices/tags/owners. Posture, routing, services, IP sets, applications, malformed capabilities, and unknown HTTP semantics reject refresh; Headscale, absent SSH, and unsupported SSH make only the Machines projection unavailable.
3. **Rule-kind-specific port behavior.** Legacy ACL ports/protocols remain unmodeled. Grants require TCP to the exact NPM backend port for services; machines require independent Grant TCP/22 plus supported SSH evidence. Non-TCP-only Grants produce neither applicable service nor machine evidence.
4. **Unproven join.** NPM `ForwardHost` is the join key, but real values may be Docker names while Headscale destinations resolve to IPs/tags.
5. **NPM access lists unused.** The runtime does not fetch or use them for visibility.
6. **Identity adapters absent.** Authentik/Authelia/`Remote-User`/`X-Webauth-*` are not accepted.
7. **Host-loopback trust boundary.** Host processes or host-network containers that can reach loopback can share the Docker-gateway source identity trusted for Serve ingress.
8. **Restricted HTTP is topology-dependent.** The hostname allowlist does not prove that the real Docker bridge is private, that Headscale port `8080` is not published elsewhere, or that direct LAN routing is absent.
9. **NPM control-proxy exposure.** NPM can observe Headscale control traffic and operator Bearer API keys and becomes required for new-client enrollment and workstation operations.
10. **Headscale automatic HTTPS Serve is future work.** Track upstream issue #2527 and PR #3300; canonical tailnet HTTP Serve remains acceptable over WireGuard.
11. **Endpoint/control-plane compromise remains in scope.** WireGuard prevents ordinary on-path LAN/router/ISP interception, not compromise of clients, TrueNAS, Tailscale/Headscale control components, NPM, or trusted host workloads.
12. **Presentation metadata is explicit and narrow.** Version 1 remains name/URL compatible; version 2 adds category/order for already-authorized cards only. It cannot create, hide, or enable cards, alter joins or health, or grant visibility. Wildcard-only cards remain unlinked until NPM has a concrete name or metadata supplies a URL. The only personalization is a per-identity browser-local fixed-logo visibility preference reached through an accessible settings panel, opaque-SHA-256-scoped, with an optional `PORTAL_LOGO_DEFAULT` deployment default; arbitrary service logos, access history, and server-side/account-synchronized preference storage remain deferred.
13. **The browser SSH console action is role-gated navigation, not a session.** It appears only for a direct Tailscale member holding an automatic-admin-equivalent role (Owner, Admin, IT admin, Network admin) on an eligible machine card, opens a fixed filtered `console.tailscale.com` admin page in a new tab using a target that passes the same narrow validation as copy commands, and never constitutes a session, proxy, enforcement check, or reachability claim. Tailscale remains responsible for console eligibility, reauthentication, SSH policy, device posture, account choice, and the session itself.
14. **Backend health is narrow observational evidence.** It is explicit opt-in, shared, memory-only, and direct-backend. It is not NPM route state, authorization, browser-path proof, persistent history, or `/healthz` readiness.
15. **Hostname suggestions are private proposals only.** They use selected-provider names plus optional bounded hostname-only stdin, reject ambiguous graph components, vanish at process exit, and never update active metadata or authorization.
16. **SSH Machines is preview evidence only.** It is Tailscale-only and absent from validation schema v3. Headscale, absent SSH, and unsupported SSH omit the complete section and per-identity Doctor previews; supported zero-match identities retain an explicit empty portal state. Server-side copy commands exist only for validated literal accounts; `autogroup:nonroot` offers only a purely client-side, client-validated custom-account input that Velociportal never sees or verifies. It proves neither Tailscale SSH enforcement nor reachability. Arbitrary FQDNs never become copied targets.
17. **Production stack preflight is read-only.** `doctor --stack-env` validates image-reference shape, subnet/gateway containment, and trusted-proxy consistency without registry, Docker, TrueNAS, credential, or upstream access. Same-name process-environment values are reported and validated with Docker Compose interpolation precedence. Combined `--env-file` plus `--stack-env` models the effective production Compose trusted-proxy override before normal diagnostics. It does not prove publication, immutability, Compose rendering, or live acceptance.
18. **No real end-to-end validation yet.** Fixtures and local probes are not production proof.
19. **SSH account suggestions never leave the browser.** Up to 10 validated account names typed into the `autogroup:nonroot` custom-account input are remembered per identity/browser under the same opaque SHA-256 login scope as the logo preference, purely in `localStorage`, with a settings-panel control to clear them. Velociportal never receives, stores, or synchronizes these values.

## Next work

1. Keep RC.6 publication and private live hostname-proposal acceptance as separate checkpoints; verify immutable image metadata before any TrueNAS image update.
2. Keep Tailscale labeled preview and never replace or retag a published RC.
3. Do not run the command against live TrueNAS or merge a generated proposal without a separate checkpoint. Do not change datasets, permissions, active metadata, the live tailnet policy, NPM, Headscale, Tailscale, DNS, certificates, networks, or ports without separate approval.
4. Prefer adding the real concrete hostname to the same NPM proxy host; use an approved metadata proposal only if that is unsuitable. Avoid a duplicate NPM host/card.
5. Build the next guided-deployment slice as a read-only `update-plan` that reuses the stack validators to compare current and proposed non-secret deployment values and prints the preservation/rollback checklist. Keep automatic TrueNAS mutation and credential-bearing backups out of scope.
6. Keep arbitrary per-service logos, access history, server-side/account-synchronized personalization, and delegated administration as separate future tasks.
7. Complete service-card and SSH-machine/reachability parity, unsupported HTTP-policy and SSH-suppression negatives, copied-target checks, token refresh/revocation, stale/cold recovery, Serve header replacement, LAN-negative raw-port isolation, and live proposal/manual-merge non-effect checks. Retain Tailscale `preview` until all live acceptance requirements pass.

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
