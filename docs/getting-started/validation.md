# Validate a real deployment

Velociportal's automated tests prove its supported parser and matcher behavior. They do **not** prove that your real Headscale policy, NPM `forward_host` values, and identity-proxy route describe the same services.

The `validate` command turns one live upstream snapshot into an explainable, privacy-controlled report. It compares at least two labeled identities and shows why supported ACL destinations did—or did not—join to NPM proxy hosts.

<div class="vp-chip-row" aria-label="Validation scope">
<span class="vp-chip vp-chip--supported">Live API snapshot</span>
<span class="vp-chip vp-chip--security">Summary privacy by default</span>
<span class="vp-chip vp-chip--validation">Human reachability checks still required</span>
</div>

!!! warning "Visibility evidence, not authorization proof"
    The report evaluates Velociportal's supported legacy ACL subset. It cannot impersonate a human tailnet identity, prove that the final proxy injected a verified login, or establish that a visible service is reachable. Headscale, the proxy, NPM, and the backend remain the enforcement boundaries.

## Generate the first report

Choose two opaque labels for real identities with intentionally different Headscale policy membership. Read the logins into temporary shell variables so they are not written literally into shell history:

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

## Complete the real validation worksheet

Record opaque labels and outcomes—not passwords, API tokens, raw response bodies, or personal profile data.

### Candidate and upstreams

- [ ] Velociportal version, Git revision, and clean/dirty source state recorded
- [ ] Image digest recorded if testing a container image
- [ ] Headscale version recorded
- [ ] NPM version recorded
- [ ] Exact trusted proxy source observed by the container recorded
- [ ] Summary report retained with owner-only permissions

### Identity path

- [ ] `user-a` visited through the final identity-aware proxy
- [ ] `user-b` visited through the final identity-aware proxy
- [ ] Their actual card sets were recorded as opaque service IDs
- [ ] Caller-supplied `Tailscale-User-*` headers were stripped or overwritten by the proxy
- [ ] A trusted-source request without `Tailscale-User-Login` returned `401`
- [ ] A request from outside `TRUSTED_PROXY_CIDR`, even with a forged login header, returned `403`
- [ ] Same-host behavior was interpreted using the documented Docker-gateway trust limitation rather than assumed to be an untrusted-source test

### Join and links

For every enabled NPM proxy host:

- [ ] Classify `forward_host` as an IP, FQDN, short/Docker name, localhost, or other
- [ ] Record the supported selector kind that joined it, or mark it unmatched
- [ ] Check every generated card's browser-facing scheme and hostname
- [ ] Note records with multiple domains; only the first currently becomes a card

### Reachability parity

For representative visible **and hidden** service pairs, keep these observations separate:

| Label | Service ID | Predicted visible | Headscale reachable | NPM frontend reachable | Notes |
|---|---|---:|---:|---:|---|
| `user-a` | `service-___` |  |  |  |  |
| `user-b` | `service-___` |  |  |  |  |

Investigate both mismatch directions:

- **visible but not Headscale-reachable** — possible ignored port/protocol, join, or infrastructure mismatch;
- **hidden but Headscale-reachable** — possible unsupported policy construct, identity mismatch, or incomplete join.

## Decide the next engineering step

The first real exercise should end with one explicit decision:

1. **Retain `forward_host`** — real values consistently match supported Headscale destination forms.
2. **Replace the join in a follow-up sprint** — real NPM values are systematically Docker names or another incompatible form.
3. **Resolve an upstream blocker first** — for example NPM 2FA authentication, a changed API response shape, Grants-only policy, or an identity proxy that cannot establish trusted `Tailscale-User-*` headers.

Do not add ambient DNS guessing or a mapping database until the real report and worksheet show which relationship is actually missing.
