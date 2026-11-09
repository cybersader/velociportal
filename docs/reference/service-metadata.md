# Service metadata and editing

**Published RC.18 adds browser-local editing, `PORTAL_EDIT_MODE` and the separate personal catalog below to RC.17's metadata v3/shared editor. RC.18 has a startup panic when personal services or shared editing is enabled.** The RC.19 corrective candidate fixes that production route conflict; use only an independently verified corrected RC.19-or-newer image after separate deployment approval. This source snapshot does not establish RC.19 publication or live recovery. These features change presentation for services a viewer can already see; they do not change policy, NPM, DNS, backend targets, SSH evidence or access. This documentation performs no deployment, provisioning or migration.

## Policy first, server defaults

`SERVICE_METADATA_FILE` is the optional server presentation configuration source. Entries associate with existing positive NPM `proxy_host_id` values **after** supported policy matching. Metadata cannot create, hide or enable cards, repair a domainless host, or alter health. A concrete NPM domain remains the preferred default; a wildcard-only card remains visible without a usable link until an explicit URL is supplied.

The strict reader accepts:

| Version | Fields in each service entry |
|---|---|
| 1 | `proxy_host_id`, optional `name` and `url` |
| 2 | v1 plus optional `category` and `order` |
| 3 (RC.17) | v2 plus optional finite `icon` ID |

All documents require exact `version` and `services` keys. Unknown, duplicate, case-variant, malformed and trailing JSON is rejected. The complete file is bounded to 256 KiB and 1,024 entries. Names are canonical unpadded strings up to 120 Unicode code points; URLs are bounded to 2,048 bytes and must be validated absolute HTTP(S) URLs without credentials or wildcard hosts. Categories are canonical strings up to 64 characters; order is an integer from 0 through 1,000,000. Existing v1/v2 `name`/`url` null presence remains readable and is preserved for untouched entries. Category and order require v2 or v3; icons require v3.

```json
{
  "version": 3,
  "services": [
    {
      "proxy_host_id": 7,
      "name": "Media",
      "url": "https://media.example.com",
      "category": "Home",
      "order": 10,
      "icon": "jellyfin"
    }
  ]
}
```

Categories and order group/sort only authorized cards, with uncategorized last and deterministic name/ID fallbacks. The dashboard edits only **name, URL and icon**. Neither editing mode changes category/order, health targets, SSH or the authorized card set. Startup does not write or migrate metadata. `suggest-hostnames` emits a strict v1 **proposal fragment**; never replace a complete v2/v3 file with that fragment. Preserve category/order/icons and unrelated entries when reviewing an operator merge.

## Editing mode selection (RC.18) { #editing-mode-selection-unreleased }

`PORTAL_EDIT_MODE` accepts only canonical lowercase `off`, `browser`, or `shared`. Explicit empty, padded, case-variant and unknown values fail configuration.

| Setting | Result |
|---|---|
| Absent | Preserve RC.17 exactly: both shared settings absent disables editing; supplied settings follow existing shared validation, including failure for partial/invalid values |
| `off` | No editor controls, writer initialization or edit API; retained shared settings are ignored, not deleted; normal readonly metadata still loads |
| `browser` | Personal presentation for any trusted viewer's currently authorized cards; no allowlist, public origin, metadata file or writable mount required |
| `shared` | Require exact editor logins, canonical public origin and the existing safe single-file writer storage |

Browser mode rejects **supplied** `PORTAL_EDITORS` or `PORTAL_PUBLIC_ORIGIN`, even empty values. Ordinary readonly `SERVICE_METADATA_FILE` is allowed as the source of browser defaults. With an independently verified corrected RC.19-or-newer image and separate deployment approval, add the single line `PORTAL_EDIT_MODE: browser` to the existing Compose service's `environment` mapping; no editor directory overlay is needed. Production provider env files may instead use `PORTAL_EDIT_MODE="browser"` with their existing go-quoted-v1 convention. RC.17 itself does not implement this selector or personal editing.

## Browser-local interaction and persistence (RC.18)

The existing Edit dialog uses **Save**, **Cancel**, **Reset**, and quiet “Saved in this browser” help. No mode switch or dashboard banner is added. Empty fields remove only that field's local override. Reset removes this service's local name/link/icon choices and restores the **current server-rendered defaults**, including optional metadata, not necessarily raw NPM values. It never modifies the server file or anyone else's browser.

The browser stores only user-entered name/URL/finite-icon overrides in a versioned map by canonical positive service ID: at most 1,024 entries and 256 KiB serialized UTF-8. Its key uses the existing opaque SHA-256 digest of the exact login, never the raw login. Browser origin supplies deployment separation. There is **no cross-device or cross-browser persistence**, account synchronization or server backup. Clearing site storage removes these choices; private windows and blocked/cleared storage may have none. Logo and SSH-account suggestions remain separate browser-local preferences.

Loaded data and saves are both untrusted and validated: exact field types/allowed fields, positive safe-integer IDs, 120-code-point names, 2,048-byte HTTP(S) URLs without userinfo/controls/padding, and finite icon choices. The browser deliberately rejects ambiguous WHATWG normalization such as shorthand IPs, host-case/default-port rewrites and dot segments; it is a conservative subset, not an assertion that Go and browser URL parsers are identical. Names update as text, icon paths come only from the fixed rendered registry, and link updates use safe DOM operations rather than stored HTML.

Only current authorized DOM cards receive preferences. Stored IDs never create cards or reveal missing topology; absent-card preferences may remain inert within the bounds. Positive NPM ID reuse can inherit old presentation, never authorization: reset/remove obsolete choices when IDs are repurposed. The browser stores no catalog, policy, access decisions, credentials or shared form tokens and makes **zero edit API calls**. Normal server polling still updates authorization.

Each fresh fragment supplies new immutable server defaults, then local preferences are applied and search is recomputed. Personal URLs update visible/search domains and linked/unlinked navigation without affecting probes. Local names do not reorder server categories or cards. Drafts survive harmless polling/search filtering; search-hidden is not unauthorized. Identity, mode or editing-availability changes invalidate the controller and require a full reload; loss of the current service clears its draft. Same-origin cross-tab events do not replace dirty forms, and saves reread the current validated map to preserve unrelated entries. Simultaneous writes are not transactional or promised conflict-free.

Unavailable, full or corrupt storage does not break browsing. Failed saves keep the draft and state **not saved**, never claim persistence or silently overwrite corrupt data. Browser blank/reset still requires working, valid storage; operator-cleared site storage restores defaults but also removes other preferences at that origin. Synthetic DOM/controller fixtures do not prove native dialog focus, layout, keyboard/touch or live behavior.

## Personal tailnet services (RC.18) { #personal-tailnet-services-unreleased }

Personal entries are **separate from NPM presentation metadata and browser-local overrides**. They support ordinary Tailscale-native HTTPS applications without an NPM host, but do not remove the selected-provider-plus-NPM dependency, alter `/healthz`, or administer upstreams. RC.17 includes neither this catalog nor browser mode.

### Enablement and use

`PORTAL_PERSONAL_SERVICES_ORIGIN` is an independent opt-in, available only with `CONTROL_PLANE=tailscale`. Its value must be the exact canonical HTTP(S) origin already used to open this portal: for example, the placeholder `http://portal.tailnet.ts.net:8081`, without a path, query, fragment or trailing slash. Absence disables store/routes/controls; supplied empty/invalid values or Headscale fail configuration. It is not an application destination, a new listener or an instruction to change Serve/DNS/TLS. An independently verified corrected RC.19-or-newer image and a separately approved deployment change are needed; no live configuration is performed here.

No file, database, writable mount, extra container, `PORTAL_EDITORS` or shared writer is needed. `PORTAL_EDIT_MODE` still independently selects NPM presentation editing: `off` does not disable an explicitly enabled personal catalog, and `browser` still never sends NPM edit requests. Shared mode retains its existing allowlist/origin/storage guards.

Use **Add service** near Services for a name, normal launch link and optional finite embedded icon. Personal cards have their own Edit action; **Your services** in account settings also manages edit/delete and recovery, including saved entries not currently displayed. Inactive entries have coarse status and no clickable bypass link. Creation/editing/recovery require JavaScript; eligible server-rendered links already in RAM remain usable without it. Personal IDs are opaque and separate from positive NPM IDs; NPM metadata, category/order, health and shared-edit APIs do not join to them.

### Links and current policy evidence

Links are canonical HTTPS URLs to a current device's canonical `*.ts.net` hostname, with an optional validated nondefault port and application path. Names are bounded to 120 Unicode code points and links to 2,048 bytes. IP fallbacks, arbitrary domains, userinfo, queries, fragments, controls, padding and unsafe normalization are rejected. **Never save pairing URLs, pairing codes or other secrets.** Query/fragment exclusion rejects common pairing-link forms but cannot detect every secret someone might embed in a path; use the normal launch address, and let the app handle its own pairing/sign-in.

Every render resolves that hostname to exactly one current normalized device and requires a positive network Grant permitting TCP to the link's ingress port (443 by default), for the exact authoritative direct member's supported login/group/role evidence. Legacy ACLs are not port evidence. Device ownership, tags on a person's devices, SSH policy/capability and NPM are not substitutes. Hostname binding follows the current device with that exact name, including a replacement that acquires it; missing, renamed, malformed or ambiguous names suppress the active card. Cold, future-dated or stale complete-snapshot evidence (older than three polling intervals) suppresses personal cards without changing existing NPM stale-render behavior. Authored inactive records remain manageable. A card is not enforcement, a health check, a reachability guarantee or proof of application login.

### Volatile availability, conflicts and explicit recovery

The existing process retains at most **128 initialized collections**, including initialized-empty collections; each versioned document allows **128 entries and 64 KiB** canonical serialized data. GET does not allocate collections. Capacity exhaustion fails explicitly rather than silently evicting another user's latest catalog. Collections are private to the same exact trusted login, not shared with other tailnet users. They are not encrypted from the trusted server/host operator.

During that process lifetime, same-login devices can read the latest acknowledged catalog without another browser being online. Existing portal polling announces generation/revision changes; there is no separate sync service or polling subsystem. Each mutation compares process generation and collection revision atomically. A concurrent change conflicts: preserve the draft, reload/review, and save deliberately. There is no automatic overwrite, merge, last-arrival-wins, offline commit queue or automatic mutation retry. A timeout after POST is uncertain; the controller reads back state to reconcile rather than blindly repeating the write.

Each browser may retain the latest confirmed **authored document** plus format/generation/revision under the opaque exact-login digest and origin. Load/save validates untrusted data and guards storage errors. No raw login, CSRF/session token, policy, device topology, rendered card catalog or authorization decisions are stored. This is an explicit authored-data recovery copy, not a PWA/authorization cache. NPM browser-local overrides, logo and SSH-account preferences remain separate and unsynchronized. A server save can succeed while browser backup fails; the UI distinguishes those outcomes.

**Restart/redeploy loses the server RAM catalog.** An absent server collection never automatically uploads or erases a retained browser copy. **Restore from this browser** requires explicit action and the same revision/generation guard; a competing create/restore conflicts. A prior-generation copy remains until explicit restore or adoption of the current server collection. A browser's copy may be stale: Velociportal cannot prove it is newest. If all copies are lost, recovery is impossible. Deleting every entry leaves initialized-empty authoritative state, which must not resurrect old entries from a recovery copy.

No guaranteed crash durability, history, backup service, account-synchronized NPM preferences, iroh, MCP, CRDT or generic bookmark/plugin framework is provided. Synthetic controller/server fixtures do not establish native layout/focus/keyboard/touch or real two-device/restart acceptance; those remain separate pending checks.

## Optional exact-login shared editors (RC.17)

Shared mode retains the existing guards. Both settings absent keep RC.17 read-only behavior; partial configuration fails. Both present require an existing valid `SERVICE_METADATA_FILE` in safe, separately provisioned writable storage.

| Setting | Contract |
|---|---|
| `PORTAL_EDITORS` | Strict JSON array, at most 32 exact full logins, each at most 254 bytes; no padding, controls, duplicates, aliases, case folding or inferred roles |
| `PORTAL_PUBLIC_ORIGIN` | Exact canonical browser-facing HTTP(S) origin, including non-default port when used; no path, query, fragment, userinfo, wildcard or trailing slash |

An empty editor array configures storage but grants nobody editing. RC.17 disables editing by **removing both settings**, not empty strings. The RC.18 selector additionally allows explicit `off` without deleting retained settings. Tailnet HTTP Serve remains supported; no HTTPS infrastructure change is implied.

The narrow setup subcommand validates these settings, preserving other environment entries:

```bash
velociportal setup service-editor --env-file velociportal.env --editors '["alice@example.com"]' --origin http://portal.tailnet.ts.net:8081
```

In the RC.18 extension setup also sets `PORTAL_EDIT_MODE=shared`. `SERVICE_METADATA_FILE` must already be set; when Compose supplies it, use the corresponding fixed runtime path in the operator configuration too. Setup never provisions or probes metadata storage. Logins in arguments can be visible to trusted same-host administration tools/history; authenticated private env editing is an alternative. Disable without changing metadata:

```bash
velociportal setup service-editor --env-file velociportal.env --disable
```

The RC.18 disable path removes both shared keys **and sets `PORTAL_EDIT_MODE=off`**. Doctor reports browser-local mode without writer/storage inspection, or coarse shared read-only prerequisites. It creates no editor lock/probe files, performs no write test and prints no editor identities/origin or saved URLs in that diagnostic. Passing is not lifetime-lock, durability or save proof. Other Doctor upstream checks remain networked.

Only configured exact logins receive shared Edit buttons, including wildcard cards. Ordinary service links remain useful without JavaScript. The native dialog/mobile sheet has explicit labels, finite icon preview, **Save for everyone**, Cancel and confirmed **Reset name/link/icon**. Blank/reset removes only shared name/link/icon, restoring NPM/generic defaults for everyone; category/order and unrelated entries remain. An entry disappears only when no fields remain. The first confirmed explicit save serializes the **complete preserved document as deterministic v3**, including unseen entries; subsequent saves do not downgrade it.

Shared drafts survive harmless polling/search and clear on identity or editing/service-visibility loss. Whole-file/target conflicts keep the draft and disable saving until explicit reload/review, never per-field merge or silent overwrite. Save is announced only after confirmation; other viewers update on ordinary polling/reload. Shared drafts, form tokens and search are memory-only, never browser storage or URL parameters.

The API requires trusted-proxy identity, unique raw identity headers, exact configured login/Host and a currently visible positive ID. POST additionally requires exact Origin, a 30-minute identity/origin-bound HMAC CSRF token, acceptable Fetch Metadata, exact JSON content type and a strict body at most 16 KiB. Missing/invisible IDs are indistinguishable. Responses are no-store/frame-denied with no CORS. Fresh complete authorization is checked immediately before save using the existing three-poll-interval cutoff; this is **not transactional upstream-revocation enforcement**. Metadata never mutates authorization state.

## Finite embedded icons

Available IDs are `generic`, `adguard-home`, `gitea`, `grafana`, `home-assistant`, `immich`, `jellyfin`, `nextcloud`, `nginx-proxy-manager`, `paperless-ngx`, `portainer`, `qbittorrent`, and `uptime-kuma`. An empty shared icon restores generic; an empty browser override restores the current server icon, including metadata. Selection is explicit, never inferred from a private hostname. No uploads, arbitrary paths, external icon URLs, favicon fetches or runtime logo-service calls exist.

The twelve unmodified PNGs come from [Dashboard Icons revision 57e939e](https://github.com/homarr-labs/dashboard-icons/tree/57e939e504eda0ea764098015da93aa666ad6f31), bounded, decoded and visually reviewed before embedding. `assets/service-icons/provenance.json` records exact upstream paths, sizes and SHA-256 digests; `LICENSE` and `ATTRIBUTION.txt` retain Apache-2.0 attribution. The generic SVG is an original inert project asset. Trademarks remain their owners' property; licensing grants no trademark rights, affiliation or endorsement. Arbitrary logos, access history and account-synchronized personalization remain deferred.

## Shared storage, conflicts and recovery

One process owns the single file and stable adjacent lifetime lock. Require a regular non-symlink, single-link target owned by the runtime effective UID with owner read/write and no group/other write; its dedicated directory must also be runtime-owned, owner-rwx and not group/other writable. Ancestors cannot be symlinks or non-sticky group/other-writable directories. No chmod/chown/ACL repair occurs. Do not replace/unlink a live lock inode.

Saves use whole-file byte revisions: **any** intervening edit, even unrelated, conflicts. A separate opaque revision detects changed NPM targets/defaults but ignores poll timestamps. No fingerprint/generation/tombstone is persisted. Positive IDs are not stable identities: review/remove obsolete metadata when deleting/reusing an NPM ID. Reuse can receive old presentation, never new authorization.

A save creates an exclusive adjacent `0600` temporary file, writes and fsyncs, rechecks disk/current visibility/target, renames, fsyncs the directory, then publishes confirmed immutable memory. Before-rename failures preserve the original file. Directory fsync failure **after rename** is uncertain: writes are disabled and rendering keeps last confirmed memory until restart/reconciliation. Never claim disk rollback, blindly retry or delete synchronization files.

Live external editing is unsupported while the dashboard writer runs. Drift causes conflict; byte rechecks are **not race-proof against uncooperative external writers**. Stop the writer, privately merge/review the complete file preserving every field and ownership/mode, then restart. Startup validates/syncs disk before enabling editing, without migration. For uncertain saves, stop, reconcile disk against a reviewed private backup/intended save, then restart with a valid known document.

**Before first v3 save**, separately approve private backup/restore and compatibility rollback. RC.16 is v1/v2-only: rollback requires compatible metadata **and editing-disabled configuration**, not image alone. Do not just lower the version with v3 fields retained. Restore a reviewed v1/v2 backup or produce a compatible document privately, reviewing presentation changes. RC.17 already supports v3/shared editing but not the new mode selector; browser localStorage is not sent to that binary or migrated into its file. No production backup/transfer/migration is performed here.

For unchanged non-root/private-port mount alternatives, see [TrueNAS shared editing](../guides/truenas-scale.md#optional-shared-service-editing).
