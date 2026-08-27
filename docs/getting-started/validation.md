# Validate a real deployment

Velociportal's automated tests prove its supported parser and matcher behavior. They do **not** prove that your selected control plane, NPM `forward_host` values, and identity-proxy route describe the same services. Headscale is the supported implementation path; Tailscale SaaS remains a labeled preview.

The `validate` command turns one live upstream snapshot into an explainable, privacy-controlled report. It compares at least two labeled identities and shows why supported ACL destinations did—or did not—join to NPM proxy hosts.

<div class="vp-chip-row" aria-label="Validation scope">
<span class="vp-chip vp-chip--supported">Live API snapshot</span>
<span class="vp-chip vp-chip--security">Summary privacy by default</span>
<span class="vp-chip vp-chip--validation">Human reachability checks still required</span>
</div>

!!! warning "Visibility evidence, not authorization proof"
    The report evaluates `legacy_acl_visibility_v1`. It cannot impersonate a human tailnet identity, prove that Tailscale Serve injected a verified login, or establish that a visible service is reachable. The selected control plane, Serve, NPM, and the backend remain the enforcement boundaries.

## Quickstart acceptance comes first

For the production one-service bundle, record these manual results before using the source-based report tools below:

- The selected Compose interface/version is 2.33.1+ and Docker Engine is 28.0+.
- The immutable Velociportal image tag and digest, plus evidence that deployment pulled it rather than reusing a cached tag.
- `CONTROL_PLANE` is explicit and the schema-v2 report records the expected provider, `legacy_acl_visibility_v1`, support label, and `selection: explicit`.
- `velociportal-upstreams` exists as a normal private bridge (`Internal=false`); NPM uses its exact alias. Headscale also uses its exact alias only in Headscale mode.
- The raw Velociportal host port is unreachable through the NAS LAN address. In Headscale mode, every current or previous Headscale host publication is also unreachable.
- In Headscale mode, the external NPM endpoint, WebSocket/upgrade behavior, authorization-header logging posture, separate keys, and expired bootstrap key all pass.
- In Tailscale mode, the dedicated OAuth client has exactly the four documented scopes; live policy/users/devices reads, token refresh, revocation, replacement, and credential redaction pass.
- Tailnet HTTP Serve returns after restarting Velociportal, Tailscale, NPM, the selected local control-plane components, and the NAS.
- Two real human identities receive intentionally different card sets.
- A caller-supplied `Tailscale-User-Login` sent through Serve does not change the authenticated identity.
- Every card is compared with actual selected-control-plane and NPM reachability.
- Tailscale preview acceptance proves unsupported Grants, posture, IP-set, and service policies fail closed.
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
- raw ACL source and destination values;
- credentials, JWTs, configuration values, and upstream payloads.

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

Private output can include internal domains, forward hosts, ports, ACL selectors, group names, and generated URLs. It still omits identity logins and credentials. The command prints a warning to stderr so redirected report data remains parseable.

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

Schema v2 adds this non-sensitive metadata:

```json
{
  "control_plane": {
    "provider": "headscale",
    "policy_mode": "legacy_acl_visibility_v1",
    "support_level": "supported",
    "selection": "explicit"
  }
}
```

Tailscale reports use `provider: "tailscale"` and `support_level: "preview"`. Existing v0.2 Headscale configuration without `CONTROL_PLANE` records `selection: "implicit"`, emits a deprecation warning, and adds a non-failing finding; set the selector explicitly before retaining acceptance evidence.

## Complete the real validation worksheet

Record opaque labels and outcomes—not passwords, API tokens, raw response bodies, or personal profile data.

### Candidate and upstreams

- [ ] Velociportal version, Git revision, and clean/dirty source state recorded
- [ ] Immutable image tag and digest recorded
- [ ] Compose interface/version recorded and confirmed as 2.33.1+ (TrueNAS, Dockge, Dokploy, or CLI)
- [ ] Docker Engine version recorded and confirmed as 28.0+
- [ ] Selected provider, provider/runtime versions, NPM version, and Tailscale app version recorded
- [ ] Schema-v2 `provider`, `policy_mode`, `support_level`, and explicit selection recorded
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
- [ ] Non-empty Grants, posture, IP-set, and service-selector policies fail the complete refresh
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

### Join and links

For every enabled NPM proxy host:

- [ ] Classify `forward_host` as a tailnet IP, routed LAN IP, FQDN, short/Docker name, localhost, or other
- [ ] Confirm the destination is reachable through Headscale; for LAN IPs, record the advertised/approved route and client route acceptance
- [ ] Record the supported selector kind that joined it, or mark it unmatched
- [ ] Check every generated card's browser-facing scheme and hostname
- [ ] Note records with multiple domains; only the first currently becomes a card

### Reachability parity

For representative visible **and hidden** service pairs, keep these observations separate:

| Label | Service ID | Predicted visible | Control-plane reachable | NPM frontend reachable | Notes |
|---|---|---:|---:|---:|---|
| `user-a` | `service-___` |  |  |  |  |
| `user-b` | `service-___` |  |  |  |  |

Investigate both mismatch directions:

- **visible but not control-plane-reachable** — possible ignored port/protocol, join, or infrastructure mismatch;
- **hidden but control-plane-reachable** — possible unsupported policy construct, identity mismatch, or incomplete join.

## Decide the next engineering step

The first real exercise should end with one explicit decision:

1. **Retain `forward_host`** — real values consistently match supported selected-control-plane destination forms.
2. **Replace the join in a follow-up sprint** — real NPM values are systematically Docker names or another incompatible form.
3. **Resolve an upstream blocker first** — for example NPM 2FA authentication, a changed API response shape, Grants-only policy, or an identity proxy that cannot establish trusted `Tailscale-User-*` headers.

Do not add ambient DNS guessing or a mapping database until the real report and worksheet show which relationship is actually missing.

No public support claim is warranted until the full control-path, private-bridge, identity, LAN-negative, restart, backup/restore, join, link, and reachability acceptance passes. Router replacement should require restoring ordinary DNS and routing only; no CA or durable application state belongs on the router.
