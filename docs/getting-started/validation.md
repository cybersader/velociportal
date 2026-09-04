# Validate a real deployment

Velociportal's automated tests prove its supported parser and matcher behavior. They do **not** prove that your selected control plane, NPM `forward_host` values, and identity-proxy route describe the same services. Headscale is the supported implementation path; Tailscale SaaS remains a labeled preview.

The `validate` command turns one live upstream snapshot into an explainable, privacy-controlled report. It compares at least two labeled identities and shows why supported access-rule destinations did—or did not—join to NPM proxy hosts.

<div class="vp-chip-row" aria-label="Validation scope">
<span class="vp-chip vp-chip--supported">Live API snapshot</span>
<span class="vp-chip vp-chip--security">Summary privacy by default</span>
<span class="vp-chip vp-chip--validation">Human reachability checks still required</span>
</div>

!!! warning "Visibility evidence, not authorization proof"
    The report evaluates HTTP services in the selected supported mode: Headscale `legacy_acl_visibility_v1`, or Tailscale `legacy_acl_visibility_v1`/`network_access_visibility_v1`. It cannot impersonate a human tailnet identity, prove that Tailscale Serve injected a verified login, establish that a visible service is reachable, or validate a machine card/SSH session. The selected control plane, Serve, Tailscale SSH, NPM, destination OS, and backends remain the enforcement boundaries.

## Quickstart acceptance comes first

For the production one-service bundle, record these manual results before using the source-based report tools below:

- The selected Compose interface/version is 2.33.1+ and Docker Engine is 28.0+.
- The immutable Velociportal image tag and digest, plus evidence that deployment pulled it rather than reusing a cached tag.
- `CONTROL_PLANE` is explicit and the schema-v3 report records the expected provider, policy mode, support label, access-rule provenance, and `selection: explicit`.
- `velociportal-upstreams` exists as a normal private bridge (`Internal=false`); NPM uses its exact alias. Headscale also uses its exact alias only in Headscale mode.
- The raw Velociportal host port is unreachable through the NAS LAN address. In Headscale mode, every current or previous Headscale host publication is also unreachable.
- In Headscale mode, the external NPM endpoint, WebSocket/upgrade behavior, authorization-header logging posture, separate keys, and expired bootstrap key all pass.
- In Tailscale mode, the dedicated OAuth client has exactly the four documented scopes; live policy/users/devices reads, token refresh, revocation, replacement, and credential redaction pass.
- Tailnet HTTP Serve returns after restarting Velociportal, Tailscale, NPM, the selected local control-plane components, and the NAS.
- Two real human identities receive intentionally different card sets.
- A caller-supplied `Tailscale-User-Login` sent through Serve does not change the authenticated identity.
- Every card is compared with actual selected-control-plane and NPM reachability.
- Tailscale preview acceptance proves supported ACL/Grant coexistence and exact TCP/backend-port matching, while posture, IP-set, service, routing, application-capability, malformed, and unknown semantics fail closed.
- When SSH Machines is exercised, each visible machine has both supported SSH-policy evidence and Grant TCP/22 evidence for the same exact direct member and device; legacy ACL port 22 never substitutes, unavailable projections omit the complete section without changing HTTP service cards, and copied targets match the intended device.
- NPM certificate/configuration and Headscale/policy backups have a tested restore path.

The [TrueNAS Quickstart](../guides/truenas-scale.md) defines the canonical topology. The remaining sections use the local-source `make validate` workflow for deeper join evidence; they are advanced diagnostics rather than a prerequisite for importing the production Compose bundle. Tailnet HTTP over WireGuard prevents ordinary on-path LAN/router/ISP interception but does not remove endpoint, NPM, host, or control-plane compromise from scope.

## Generate the first report

Choose two opaque labels for real identities with intentionally different selected-control-plane policy membership. Read the logins into temporary shell variables so they are not written literally into shell history:

```bash
read -r -s -p 'Login for user-a: ' VP_USER_A; printf '\n'
read -r -s -p 'Login for user-b: ' VP_USER_B; printf '\n'
make validate VALIDATE_ARGS="\
  --identity user-a=${VP_USER_A} \
  --identity user-b=${VP_USER_B}"
unset VP_USER_A VP_USER_B
```

The Make target does not echo the expanded container command. The labels appear in the report; the logins do not. While validation is running, however, the supplied logins remain command-line arguments and may be visible to same-host process or container inspection tools. Run validation only on a trusted administrative host. Do not put passwords, API keys, or tokens in `VALIDATE_ARGS`.

A report exits:

- **`0`** when collection completed and no automated finding requires review;
- **`1`** when collection failed **or** a complete report contains unresolved findings;
- **`2`** for invalid command usage.

An application exit code of `1` can therefore accompany a useful report. Read its `Findings` section before deciding what failed. GNU Make reports any failed recipe as Make exit code `2`, so `make validate` and `make validate-json` can return `2` when the underlying validation command returned `1`; the report's `status` and findings remain authoritative.

## Read the summary report

Summary privacy is the default. It emits deterministic opaque service IDs such as `service-001` and keeps relationships needed for comparison while omitting:

- identity logins;
- domains and card URLs;
- node names and addresses;
- NPM forward targets;
- raw access-rule source and destination values;
- credentials, JWTs, configuration values, and upstream payloads.

Schema-v3 validation currently reports HTTP service evidence only. It does not serialize SSH-machine policy, device, account, target, or command evidence. Doctor reports per-identity machine counts only when Tailscale is selected and the SSH projection is present and supported; Headscale, absent SSH, and unsupported SSH suppress those previews, while the top-level unsupported-policy diagnostic remains. Doctor deliberately omits all machine topology and account details. Validate Machines through the private portal and the manual worksheet below rather than publishing a topology report.

Summary output is **not anonymous**. An opaque access matrix can still reveal operational relationships, so store and share it deliberately.

The report separates:

1. **Structural join evidence** — whether an enabled NPM forward target matches an identity-independent supported destination form.
2. **Identity matcher evidence** — which opaque services each supplied identity label receives and which supported selector kind matched.
3. **Manual observations** — proxy behavior and actual network reachability that only an operator can verify.

Common review findings include:

| Finding | Meaning |
|---|---|
| `untraceable-build` | The binary cannot be tied to a known clean source revision |
| `unmatched-forward-host` | No supplied identity or identity-independent supported destination matched that service |
| `zero-card-identity` | One supplied identity produced no cards |
| `identical-card-sets` | All supplied identities produced the same set; confirm this is intentional |
| `enabled-host-without-domain` | An enabled NPM record could not become a card |

Notices such as `browser-scheme-unverified` do not fail the report, but they still require manual deployment checks.

## Diagnose with private detail

Use private mode only when you need the real values behind an opaque service:

Repeat the hidden `read` commands from the first example, then run:

```bash
make validate VALIDATE_ARGS="\
  --identity user-a=${VP_USER_A} \
  --identity user-b=${VP_USER_B} \
  --privacy private"
unset VP_USER_A VP_USER_B
```

Private output can include internal domains, forward hosts, ports, access-rule selectors, rule provenance, group names, and generated URLs. It still omits identity logins and credentials. The command prints a warning to stderr so redirected report data remains parseable.

!!! danger "Do not publish private reports"
    Private validation output maps internal topology and policy relationships. Keep it owner-readable, do not attach it to public issues, and remove it after the join decision is recorded.

## Save deterministic JSON

Use the dedicated target so stdout contains only JSON while build and Compose progress remain on stderr:

Repeat the hidden `read` commands from the first example, then run:

```bash
umask 077
make validate-json VALIDATE_ARGS="\
  --identity user-a=${VP_USER_A} \
  --identity user-b=${VP_USER_B}" \
  > validation-summary.json
unset VP_USER_A VP_USER_B
```

The file mode is controlled by your shell's `umask`. A dirty or unknown build deliberately produces `review_required`; tie evidence to a clean revision before treating it as source-traceable. Record the tested image digest separately because source metadata alone does not make the container byte-for-byte reproducible.

Schema v3 records access-rule counts and provenance in addition to control-plane metadata:

```json
{
  "control_plane": {
    "provider": "tailscale",
    "policy_mode": "network_access_visibility_v1",
    "support_level": "preview",
    "selection": "explicit"
  },
  "snapshot": {
    "access_rules": 4
  },
  "identities": [{
    "services": [{
      "rule_kind": "grant",
      "rule_index": 1
    }]
  }]
}
```

Headscale reports use `provider: "headscale"`, `policy_mode: "legacy_acl_visibility_v1"`, and `support_level: "supported"`. Tailscale ACL-only policies also report the legacy mode; accepted Grants select `network_access_visibility_v1`. Existing v0.2 Headscale configuration without `CONTROL_PLANE` records `selection: "implicit"`, emits a deprecation warning, and adds a non-failing finding; set the selector explicitly before retaining acceptance evidence.

## Complete the real validation worksheet

Record opaque labels and outcomes—not passwords, API tokens, raw response bodies, or personal profile data.

### Candidate and upstreams

- [ ] Velociportal version, Git revision, and clean/dirty source state recorded
- [ ] Immutable image tag and digest recorded
- [ ] Compose interface/version recorded and confirmed as 2.33.1+ (TrueNAS, Dockge, Dokploy, or CLI)
- [ ] Docker Engine version recorded and confirmed as 28.0+
- [ ] Selected provider, provider/runtime versions, NPM version, and Tailscale app version recorded
- [ ] Schema-v3 `provider`, `policy_mode`, `support_level`, and explicit selection recorded
- [ ] `velociportal-upstreams` recorded as `Internal=false` with `npm.velociportal.internal`; Headscale alias recorded only for Headscale mode
- [ ] Headscale retained outbound Docker DNS, DERP retrieval, and `/health` after attachment
- [ ] NPM retained its existing listeners, outbound DNS/HTTPS, certificate operations, and management/API health after attachment
- [ ] Runtime URLs recorded as the direct private aliases; Velociportal runtime bypasses NPM
- [ ] Headscale container port `8080` confirmed `None`/`Expose` only, with no host mapping on any port; every current or previous mapped host port was tested from the LAN
- [ ] Production ingress subnet, gateway, trusted proxy `/32`, and Velociportal preferred gateway priority recorded
- [ ] Raw Velociportal port confirmed unreachable on the NAS LAN address
- [ ] Base deployment confirmed to have no CA mount; any private-CA overlay recorded as an optional alternative
- [ ] Summary report retained with owner-only permissions when generated

### Pre-tailnet Headscale control and keys

- [ ] External Headscale `server_url` is the trusted NPM HTTPS origin
- [ ] Split-horizon/private DNS resolves it for intended local enrollment, with no public Headscale A/AAAA/CNAME record
- [ ] The existing DNS-01 wildcard certificate avoids exact-host disclosure in certificate-transparency logs
- [ ] A brand-new required client verifies that certificate before joining; no insecure flag or verification bypass used
- [ ] NPM forwards to `http://headscale.velociportal.internal:8080`
- [ ] NPM WebSocket support and HTTP upgrade preservation verified
- [ ] NPM custom logs confirmed not to record `Authorization` or full request headers
- [ ] HTTPS-only `headscale-ops status` succeeds through NPM
- [ ] Operator and Velociportal runtime keys are distinct
- [ ] One-time bootstrap key is expired
- [ ] NPM database/configuration/certificate backup and restore evidence recorded

### Tailscale SaaS OAuth and policy preview

Skip this section in Headscale mode.

- [ ] Dedicated OAuth client ID recorded privately; no access token stored in configuration
- [ ] Exact scopes recorded: `policy_file:read`, `devices:posture_attributes:read`, `devices:core:read`, `users:read`
- [ ] Fixed origin `https://api.tailscale.com/api/v2` and `-` tailnet alias confirmed
- [ ] Policy, users, and devices reads succeed with the deployed credential
- [ ] Access-token refresh observed beyond the normal one-hour lifetime
- [ ] Revocation fails closed and replacement credentials recover without restart-only assumptions
- [ ] Doctor/errors contain no client ID, secret, rejected token, or replacement token
- [ ] Two exact `loginName` values map unambiguously through real device owner references
- [ ] Duplicate, blank, ambiguous, unresolved, paginated, or partial fixture cases remain fail-closed
- [ ] ACLs and accepted Grants coexist additively; Grant cards require TCP to the exact NPM backend port
- [ ] Source-tag and other machine-source Grants load without becoming human service or SSH-machine cards; attr-only Funnel `nodeAttrs` never authorize
- [ ] Padded API `loginName` values reject the complete refresh rather than manufacturing exact browser role membership
- [ ] A supported SSH section is normalized separately; Headscale, absent SSH, and one unsupported SSH rule make the projection unavailable and omit the complete Machines section without invalidating otherwise supported HTTP service cards
- [ ] Machine cards require both supported SSH and Grant TCP/22 evidence; ACL-only port 22, wrong-port, and non-TCP Grants produce no machine
- [ ] Shared-user, case-variant, short/bare-login, role-isolation, tag-ownership, and destination negatives produce no unauthorized machines
- [ ] Posture, IP-set, service, routing, application-capability, malformed, and unknown HTTP-policy semantics fail the complete refresh
- [ ] Cold-start failure, stale-snapshot retention, and recovery are recorded
- [ ] Tailscale support remains labeled preview in retained evidence

### Identity path

- [ ] Policy explicitly allowed both users to reach the Serve node on TCP port `8081`
- [ ] `user-a` visited through declarative tailnet HTTP Serve
- [ ] `user-b` visited through declarative tailnet HTTP Serve
- [ ] Their actual card sets were recorded as opaque service IDs
- [ ] Caller-supplied `Tailscale-User-*` headers were stripped or overwritten by Serve
- [ ] Serve returned after restarting Velociportal, the Tailscale app, NPM, Headscale, and the NAS
- [ ] A trusted-source request without `Tailscale-User-Login` returned `401` where that diagnostic route was deliberately constructed
- [ ] A reachable request from outside `TRUSTED_PROXY_CIDR`, even with a forged login, returned `403` where such a diagnostic route existed
- [ ] A LAN request to the raw loopback-published port failed to connect; network inaccessibility was not misreported as an application `403`
- [ ] Same-host behavior was interpreted using the documented Docker-gateway trust limitation rather than assumed to be an untrusted-source test

### SSH Machines preview

Skip this section in Headscale mode or when the Tailscale policy has no intended SSH-machine projection.

- [ ] Doctor reports `state=supported` with the expected SSH rule count and per-identity counts only when the projection is available; unsupported SSH retains one coarse top-level reason but emits no per-identity machine preview lines
- [ ] Headscale, absent SSH, and unsupported SSH omit the complete Machines section; supported SSH with zero matches preserves the explicit empty Machines state
- [ ] At least two exact direct-member identities receive intentionally different machine sets; a shared user receives none
- [ ] Every visible machine has one matching supported SSH rule and one independent matching Grant that permits TCP/22 to the same device address
- [ ] Remove or change either side of that evidence in a controlled fixture/policy test and confirm the machine disappears; an ACL mentioning port 22 does not preserve it
- [ ] Owner-to-Admin automatic membership and each specialized role are checked independently; no specialized role implies another
- [ ] `autogroup:self` includes only devices with the exact separately resolved owner login; tags and `tagOwners` do not create human source membership
- [ ] A literal allowed account produces the expected server-built `tailscale ssh user@target` copy command; `autogroup:nonroot` never invents or pre-fills an account but offers a separate client-side field for a typed validated non-root account
- [ ] The custom non-root field rejects `root`, surrounding whitespace, selectors, and shell metacharacters; successful copies remember at most 10 deduplicated names only for the exact identity/browser, survive htmx refresh, and clear from account settings
- [ ] A canonical full `*.ts.net` device name remains the copied-command target while the card and eligible Tailscale Machines link use its short first label for prominent display/search; an arbitrary FQDN is rejected and falls back only to the same device's validated Tailscale IPv4/IPv6 address
- [ ] Plain-language action labels match policy semantics: `accept` shows no extra sign-in, while `check` shows the configured reauthentication period or the documented 12-hour default
- [ ] Shell/HTML/JavaScript metacharacter fixtures never become executable markup or copied arguments
- [ ] Each copied command is run from the corresponding real identity and compared with actual Tailscale SSH outcome, including `check` behavior where configured
- [ ] A visible machine that cannot be reached or logged into is recorded as a visibility/reachability mismatch, not described as authorized or healthy
- [ ] When the projection is available, the populated or explicit-empty Machines section survives htmx refresh and remains independent of NPM service cards, metadata, category/order, and backend health

Keep the private comparison local. Record opaque labels and pass/fail outcomes rather than device names, Tailscale addresses, OS account names, or commands.

| Label | Machine ID | Predicted visible | Command offered | Tailscale SSH succeeds/checks as expected | Notes |
|---|---|---:|---:|---:|---|
| `user-a` | `machine-___` |  |  |  |  |
| `user-b` | `machine-___` |  |  |  |  |

### Join and links

For every enabled NPM proxy host:

- [ ] Classify `forward_host` as a tailnet IP, routed LAN IP, FQDN, short/Docker name, localhost, or other
- [ ] Confirm the destination is reachable through the selected control plane; for LAN IPs, record the advertised/approved route and client route acceptance
- [ ] Record the supported selector kind that joined it, or mark it unmatched
- [ ] Check every generated card's browser-facing scheme and hostname
- [ ] Confirm a wildcard followed by a concrete NPM name selects the concrete name
- [ ] Confirm wildcard-only services remain visible with `link needed` and emit no `%2A`/wildcard `href`
- [ ] Prefer adding the real concrete name to the same NPM proxy host; record any explicit metadata URL used instead
- [ ] Confirm metadata changes only presentation and does not alter the matched `forward_host`, `forward_port`, rule, or destination evidence
- [ ] Note records with multiple domains; only the first valid concrete domain becomes the automatic link
- [ ] Confirm `nginx_online` remains NPM route state and does not create a health label

### Optional hostname-suggestion proposal

When testing `suggest-hostnames` privately:

- [ ] Run it only from a trusted operator environment with `--privacy private` and an explicit browser scheme
- [ ] Confirm the review contains only canonical hostnames, coarse source classes, and existing positive NPM proxy-host IDs; independently verify that each untrusted provider-visible name is the intended browser destination
- [ ] Confirm invalid control-plane names are skipped and invalid stdin fails with a value-free line-number error
- [ ] Include a deliberate one-to-many or many-to-one wildcard match and confirm the complete connected component is excluded
- [ ] Confirm stdout stays empty before approval and on rejection, EOF, terminal failure, or any operational error before emission; use `--output` to test atomic publication because a downstream stdout failure after approval can leave a partial stream
- [ ] Confirm `--output` creates a new `0600` file and refuses the active metadata path, existing files, and symlinks
- [ ] Compare multi-identity card sets before and after proposal generation; they must be identical because generation does not apply metadata
- [ ] Manually review and merge only an approved proposal, then confirm that only display name/browser URL changed and hidden, disabled, unmatched, or domainless services did not gain cards
- [ ] Remove the private proposal after the decision is recorded

The command is not DNS discovery, a recurring observer, or an updater. It adds no production mount, route, port, runtime store, or automatic metadata mutation.

### Optional service-health observations

When the health overlay is enabled:

- [ ] Confirm only explicitly configured positive proxy-host IDs receive a status label
- [ ] Confirm unauthorized cards and their health states are absent for each identity
- [ ] Confirm a stopped configured backend becomes `unreachable` or `response error`, never green
- [ ] Confirm `401`/`403` becomes `authentication required` even if an accepted range would otherwise include it
- [ ] Confirm HTTP uses the configured path without following redirects and TCP sends no payload
- [ ] Confirm every DNS answer fits the explicit CIDRs and exact host/suffix policy; include mixed-answer and disallowed-address negatives
- [ ] Confirm NPM and selected-control-plane API sockets cannot be selected directly or through an alias
- [ ] Confirm invalid health configuration leaves the authorization/catalog snapshot and `/healthz` healthy while old observations age to `stale`
- [ ] Confirm no health path, backend hostname/IP, response body, credential, or raw network error appears in Doctor or runtime logs

Treat every label as a shared point-in-time backend observation, not proof that a particular user can reach the browser URL end to end.

### Reachability parity

For representative visible **and hidden** service pairs, keep these observations separate:

| Label | Service ID | Predicted visible | Control-plane reachable | NPM frontend reachable | Notes |
|---|---|---:|---:|---:|---|
| `user-a` | `service-___` |  |  |  |  |
| `user-b` | `service-___` |  |  |  |  |

Investigate both mismatch directions:

- **visible but not control-plane-reachable** — possible legacy-ACL port/protocol overstatement, join, or infrastructure mismatch;
- **hidden but control-plane-reachable** — possible unsupported policy construct, identity mismatch, or incomplete join.

## Decide the next engineering step

The first real exercise should end with one explicit decision:

1. **Retain `forward_host`** — real values consistently match supported selected-control-plane destination forms.
2. **Replace the join in a follow-up sprint** — real NPM values are systematically Docker names or another incompatible form.
3. **Resolve an upstream blocker first** — for example NPM 2FA authentication, a changed API response shape, unsupported modern-policy semantics, or an identity proxy that cannot establish trusted `Tailscale-User-*` headers.

Use an explicit exact-ID service metadata URL only for a confirmed presentation gap. Do not treat ambient DNS/Tailscale observations as authorization or persist them as a mapping database; any future observation feature must remain privacy-minimized, ephemeral, and operator-approved.

No public support claim is warranted until the full control-path, private-bridge, identity, LAN-negative, restart, backup/restore, join, link, and reachability acceptance passes. Router replacement should require restoring ordinary DNS and routing only; no CA or durable application state belongs on the router.
