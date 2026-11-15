# Service metadata and editing

**RC.20 candidate versus earlier releases.** RC.18 introduced browser-local editing and the personal RAM catalog but has an optional-API startup panic. Immutable RC.19 contains the route correction at `a7fd76d807c6a5d608a09634409c1b9cf322edf7`; neither contains MCP, durable personal storage or metadata v4. These opt-ins are selected for `v0.2.0-rc.20`, without asserting a published image/digest or live enablement. Remaining client/live gaps are disclosed, not RC.20 prepublication blockers. Tailscale remains preview; separate deployment, storage and migration approval is still required.

Presentation never changes policy, NPM, DNS, backend targets, SSH evidence or access. The published RAM-only behavior remains the source default when no private file is configured. See [MCP transport/tools and remaining acceptance](mcp.md).

## Policy first, server defaults

`SERVICE_METADATA_FILE` is the optional server presentation configuration source. Entries associate with existing positive NPM `proxy_host_id` values **after** supported policy matching. Metadata cannot create, hide or enable cards, repair a domainless host, or alter health. A concrete NPM domain remains the preferred default; a wildcard-only card remains visible without a usable link until an explicit URL is supplied.

The strict reader accepts:

| Version | Fields in each service entry |
|---|---|
| 1 | `proxy_host_id`, optional `name` and `url` |
| 2 | v1 plus optional `category` and `order` |
| 3 (RC.17) | v2 plus optional finite `icon` ID |
| 4 (RC.20 candidate) | Existing NPM fields plus required source binding and separate Machine/application icon arrays; see the v4 contract below |

Legacy v1–v3 documents require exact `version` and `services` keys. Unknown, duplicate, case-variant, malformed and trailing JSON is rejected. The complete file is bounded to 256 KiB and 1,024 entries. Names are canonical unpadded strings up to 120 Unicode code points; URLs are bounded to 2,048 bytes and must be validated absolute HTTP(S) URLs without credentials or wildcard hosts. Categories are canonical strings up to 64 characters; order is an integer from 0 through 1,000,000. Existing v1/v2 `name`/`url` null presence remains readable and is preserved for untouched entries. Category and order require v2, v3 or v4; icons require v3 or v4.

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

This **file-absent volatile mode** provides no crash durability or personal MCP tools. Optional durable source behavior is described below. History, a backup service, account-synchronized NPM preferences, iroh, CRDT and a generic bookmark/plugin framework remain absent. Synthetic controller/server fixtures do not establish native layout/focus/keyboard/touch or real two-device/restart acceptance; those remain separate pending checks.

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

Only configured exact logins receive shared Edit buttons, including wildcard cards. Ordinary service links remain useful without JavaScript. The native dialog/mobile sheet has explicit labels, finite icon preview, **Save for everyone**, Cancel and confirmed **Reset name/link/icon**. Blank/reset removes only shared name/link/icon, restoring NPM/generic defaults for everyone; category/order and unrelated entries remain. An entry disappears only when no fields remain. An ordinary browser save of legacy metadata serializes the **complete preserved document as deterministic v3**, including unseen entries. In unreleased source, a v4 input stays v4 losslessly; only explicit icon operations may promote legacy data to v4.

Shared drafts survive harmless polling/search and clear on identity or editing/service-visibility loss. Whole-file/target conflicts keep the draft and disable saving until explicit reload/review, never per-field merge or silent overwrite. Save is announced only after confirmation; other viewers update on ordinary polling/reload. Shared drafts, form tokens and search are memory-only, never browser storage or URL parameters.

The API requires trusted-proxy identity, unique raw identity headers, exact configured login/Host and a currently visible positive ID. POST additionally requires exact Origin, a 30-minute identity/origin-bound HMAC CSRF token, acceptable Fetch Metadata, exact JSON content type and a strict body at most 16 KiB. Missing/invisible IDs are indistinguishable. Responses are no-store/frame-denied with no CORS. Fresh complete authorization is checked immediately before save using the existing three-poll-interval cutoff; this is **not transactional upstream-revocation enforcement**. Metadata never mutates authorization state.

## Finite embedded icons

Available IDs are `generic`, `adguard-home`, `gitea`, `grafana`, `home-assistant`, `immich`, `jellyfin`, `nextcloud`, `nginx-proxy-manager`, `paperless-ngx`, `portainer`, `qbittorrent`, and `uptime-kuma`. Removing a shared icon restores the existing fallback (which may be no image); an empty browser override restores the current server icon, including metadata. In unreleased source, an empty authored personal icon inherits the shared application default, while explicit `generic` overrides it. Selection is explicit, never inferred from a private hostname. No uploads, arbitrary paths, external icon URLs, favicon fetches or runtime logo-service calls exist.

The twelve unmodified PNGs come from [Dashboard Icons revision 57e939e](https://github.com/homarr-labs/dashboard-icons/tree/57e939e504eda0ea764098015da93aa666ad6f31), bounded, decoded and visually reviewed before embedding. `assets/service-icons/provenance.json` records exact upstream paths, sizes and SHA-256 digests; `LICENSE` and `ATTRIBUTION.txt` retain Apache-2.0 attribution. The generic SVG is an original inert project asset. Trademarks remain their owners' property; licensing grants no trademark rights, affiliation or endorsement. Arbitrary logos, access history and account-synchronized personalization remain deferred.

## Shared storage, conflicts and recovery

One process owns the single file and stable adjacent lifetime lock. Require a regular non-symlink, single-link target owned by the runtime effective UID with owner read/write and no group/other write; its dedicated directory must also be runtime-owned, owner-rwx and not group/other writable. Ancestors cannot be symlinks or non-sticky group/other-writable directories. No chmod/chown/ACL repair occurs. Do not replace/unlink a live lock inode.

Saves use whole-file byte revisions: **any** intervening edit, even unrelated, conflicts. A separate opaque revision detects changed NPM targets/defaults but ignores poll timestamps. No fingerprint/generation/tombstone is persisted. Positive IDs are not stable identities: review/remove obsolete metadata when deleting/reusing an NPM ID. Reuse can receive old presentation, never new authorization.

A save creates an exclusive adjacent `0600` temporary file, writes and fsyncs, rechecks disk/current visibility/target, renames, fsyncs the directory, then publishes confirmed immutable memory. Known failures before attempting rename preserve the original file. An attempted-rename error or later directory-sync/publication failure is uncertain: all shared writes and authoritative editable read-back are disabled until validated restart/reconciliation. Last confirmed presentation may still render on already-visible cards, but is not proof of disk state. Never claim disk rollback, blindly retry or delete synchronization files.

Live external editing is unsupported while the dashboard writer runs. Drift causes conflict; byte rechecks are **not race-proof against uncooperative external writers**. Stop the writer, privately merge/review the complete file preserving every field and ownership/mode, then restart. Startup validates/syncs disk before enabling editing, without migration. For uncertain saves, stop, reconcile disk against a reviewed private backup/intended save, then restart with a valid known document.

**Before first v3 save**, separately approve private backup/restore and compatibility rollback. RC.16 is v1/v2-only: rollback requires compatible metadata **and editing-disabled configuration**, not image alone. Do not just lower the version with v3 fields retained. Restore a reviewed v1/v2 backup or produce a compatible document privately, reviewing presentation changes. RC.17 already supports v3/shared editing but not the new mode selector; browser localStorage is not sent to that binary or migrated into its file. No production backup/transfer/migration is performed here.

For unchanged non-root/private-port mount alternatives, see [TrueNAS shared editing](../guides/truenas-scale.md#optional-shared-service-editing).

## Shared icons and metadata v4 (RC.20 candidate) { #shared-icons-and-metadata-v4-unreleased-source }

V4 extends the **same** `SERVICE_METADATA_FILE`; there is no second shared presentation store. It is readable in off/browser/shared modes only with a valid `PORTAL_STATE_NAMESPACE` equal to the document's namespace and a `control_plane` equal to the selected provider. Merely configuring namespace does not enable writing or migrate legacy metadata. It binds the selected control-plane **and NPM catalog pair** by operator assertion, not a verified upstream UUID or credential.

All six root keys are required; all arrays must exist, even empty. This synthetic minimal document is valid for namespace `portal-demo-01` and Tailscale, but **do not copy the demonstration namespace as a deployment identity**:

```json
{
  "version": 4,
  "namespace": "portal-demo-01",
  "control_plane": "tailscale",
  "services": [],
  "machines": [],
  "applications": []
}
```

| Array / target | Stored key and meaning |
|---|---|
| `services` / NPM | Existing positive `proxy_host_id`; optional existing name/URL/category/order/icon fields. NPM ID reuse can inherit old presentation within a namespace; remove obsolete mappings when repurposing IDs. |
| `machines` / Machine | Exactly `device_id_type` (`id` or `nodeId`), `device_id`, finite `icon`. Source-bound typed provider identity, not name/IP. |
| `applications` / personal destination | Exactly `target_digest` (`sha256:` plus 64 lowercase hexadecimal digits), finite `icon`. Server-derived destination key, never an authored personal record ID. |

The complete file remains **256 KiB**, with **1,024 aggregate entries across all three arrays**, not 1,024 each. Unknown/duplicate/case-variant keys, null arrays, duplicate target keys, invalid Unicode/scalars, noncanonical integer forms and excessive nesting (depth 16) are rejected. V4 permits explicitly present NPM `name:null`/`url:null` to survive an icon reset even when no other optional field remains; null remains no override. Other fields cannot be null. Headscale v4 requires empty `machines` and `applications` and supplies only NPM defaults.

Stable Tailscale presentation IDs use the actual API field provenance, never a guessed type: canonical ASCII string IDs match `[A-Za-z0-9][A-Za-z0-9_-]{0,127}`; numeric API `id` accepts only nonnegative canonical decimal integer spelling, preserved as a string. Supported `id` wins; `nodeId` is used only for a genuine supported fallback. Ambiguous/unsupported IDs make the **shared artwork target unavailable**, not an otherwise authorized card invisible. A rename preserves the same typed identity; replacement/reenrollment or a changed ID kind does not inherit icons through a reused hostname/IP. Exact upstream typed-ID reuse cannot be detected as different hardware.

An application digest hashes a versioned length-framed tuple: namespace, `tailscale`, `personal_application`, typed device-ID kind/value, `https`, effective decimal TCP port and exact canonical escaped path. Default port is 443; empty/root path normalizes to `/`; `/app` and `/app/` remain different. Explicit default `:443` remains invalid authored input. Hostname, login and personal record ID are not part of this shared destination key. The shared file contains no raw application URL/path. **A digest is not encryption or guaranteed secrecy**: plausible paths can be guessed; namespace is not a secret salt. No path-level authorization is implied.

Shared icon tools require the exact editor allowlist and a current visible target. NPM uses the existing matcher; Machines require the full supported SSH + independent Grant TCP/22 + current SSH-enabled/unblocked device projection. Applications require the maintainer's **own active entry** with fresh direct-member/owner evidence, in RAM or durable mode. No foreign private-record enumeration, reverse digest lookup or matching-user count is exposed. Two owners' active entries to the same destination may intentionally receive the same default without disclosing each other's entries.

Precedence, after visibility matching:

| Card | Highest to lowest |
|---|---|
| NPM | Explicit browser-local override → shared metadata icon → existing fallback (which may be no image) |
| Personal application | Explicit authored icon → shared destination default → generic |
| Machine | Shared Machine default → existing fallback (which may be no image) |

Empty authored personal icon means **inherit**; explicit `generic` masks a shared default. Computed defaults never enter authored private records, revisions or browser recovery copies. Shared icon reset removes only that icon/default; ordinary browser NPM reset removes name/link/icon while preserving category/order. Personal blank icon removes only the owner's override. Metadata never creates or authorizes any card.

### Lossless writers and binding failure

Ordinary browser NPM save/reset keeps legacy→v3 and v4→v4. Only an explicit shared icon set/reset may promote the complete legacy file to v4, under a valid configured binding. Even explicit icon reset can promote. Every writer preserves unrelated arrays, root binding, fields and applicable null presence; no downcast/rebinding or startup rewrite occurs. An icon-only mutation does not clear name/link/category/order.

Configured v4 binding mismatch fails preflight when a readable v4 document is recognized, including off/browser mode; the bound runtime reader also rejects mismatched/invalid reloads without publishing a partial snapshot. Ordinary unreadable/malformed readonly metadata follows complete-refresh failure semantics, not automatic repair or empty fallback. Shared writer startup requires a valid bound document. A valid readonly v4 file does not initialize a lock or writer. Operator hostname proposals remain v1 fragments: merge offline while the writer is stopped, preserving the **entire** v4 document, never replacing it with a proposal.

## Durable personal storage (RC.20 candidate) { #durable-personal-storage-unreleased-source }

`PORTAL_PERSONAL_SERVICES_FILE` is an independent persistence opt-in. **Absent retains the existing browser-managed RAM mode**, including explicit possibly stale browser restoration after restart. A supplied file requires Tailscale, enabled canonical `PORTAL_PERSONAL_SERVICES_ORIGIN`, a valid common `PORTAL_STATE_NAMESPACE`, and a canonical absolute file path (no padding, controls, `..`, or root path). Missing, corrupt, wrong-bound or unsafe configured private storage **fails startup; no RAM fallback** occurs. MCP personal read/create/update/delete require durable mode. No new upstream endpoint or OAuth scope is used.

Durable collections bind to **namespace + exact trusted login + authoritative Users API user ID/direct-member status**, not device owner inference. Every private read, render and mutation requires fresh complete matching owner evidence. Removed/shared/reassigned logins cannot recover an old collection; a renamed login cannot adopt the same user ID automatically. Cold or stale snapshots withhold private content. Once ownership passes, inactive authored destinations remain manageable without active links. Active links still require unique current hostname plus positive ingress TCP Grant evidence; the disk never caches that decision.

### Private snapshot schema and limits

One whole private snapshot persists only authored data, ownership and store/revision bookkeeping. It has no policy, topology, tokens, sessions or access decisions. A collection record means initialized, including an empty document. Bounds are **128 collections including initialized-empty**, **128 entries / 64 KiB canonical authored data per collection**, and **9 MiB complete serialized snapshot**. GET never allocates and capacity never evicts another owner.

The initial file must already exist. This is a schema-valid **synthetic example, not an identity to copy into production**:

```json
{
  "version": 1,
  "namespace": "portal-demo-01",
  "store_id": "0123456789abcdef0123456789abcdef",
  "collections": []
}
```

The exact root keys are `version`, `namespace`, `store_id`, `collections`. Version is integer 1; StoreID is 32 lowercase hex characters, stable across ordinary restarts. For a **new separately approved empty store only**, generate an independent StoreID with `python3 -c 'import secrets; print(secrets.token_hex(16))'`; this prints an ID and provisions nothing. Choose and retain a canonical source namespace separately. Never regenerate either on each restart or reset an existing collection to the example.

Each nonempty `collections` element has exactly `login`, `user_id`, `revision`, `document`; revision is a positive canonical decimal uint64 **string**. `document` is the existing version-1 personal document with authored `entries`. Logins and user IDs are unique across the snapshot. Unknown/duplicate/case-variant fields, nulls, malformed Unicode, trailing JSON and depth over 16 fail validation. Do not hand-invent ownership IDs or bulk seed private entries: normal admitted creation binds authoritative ownership. There is no new initialization/import CLI or MCP bulk-restore tool.

### File safety and confirmed saves

The dedicated private directory must be owned by the runtime effective UID, **exact mode 0700**; the snapshot and any adjacent lock must be regular, single-link, same-owner files, **exact mode 0600**, without special mode bits. Neither symlinks nor hardlinks are supported. The directory walk uses held descriptors and `NOFOLLOW` for every component; ancestors must be directories owned by root or runtime UID, no setuid/setgid or group/other write. Only root-owned sticky `/tmp` or `/var/tmp` ancestors receive the narrow temporary-directory exception; the dedicated directory itself is always 0700. Container-visible ancestors are validated; host-side storage/ACL safety must also be separately reviewed.

Startup validates the preexisting file before creating its adjacent `<filename>.lock`, takes a nonblocking exclusive lifetime lock, rereads and syncs the file/lock/directory, and keeps the directory held. It does not initialize a missing snapshot, change ownership/modes or repair storage. A second process cannot share the store. Do not unlink/replace a live lock. Configured metadata/health targets and their `.lock` paths are excluded from private file/lock collisions, including existing inode aliases; ambiguous path spellings fail rather than changing the exclusion target. Use independent directories and nonoverlapping mount targets, not hardlink aliases.

Each save validates the complete bounded candidate, checks whole-file CAS, writes an exclusive adjacent 0600 temporary file, syncs/closes it, rechecks disk and concrete current ownership/transport authority, attempts adjacent rename, syncs the directory, then publishes confirmed memory. Success is not acknowledged before required sync. A known pre-rename failure leaves the old state; an attempted-rename error or later sync/publication uncertainty suppresses **all authoritative private reads and writes in browser and MCP** until validated locked reopen/reconciliation. Old RAM must not be used as proof of rollback. Confirmed reads are not external-writer detection; live external writers are unsupported. Shared storage has its own lock/failure domain, not a cross-file transaction.

Fresh process `generation` is separate from stable StoreID and persisted per-collection revision. All three participate in durable mutation CAS. Owner/request/freshness loss after a confirmed sync may withhold the response without undoing disk. Cancellation, timeout or response loss never justifies replay. Current snapshots may remain usable for up to three polling intervals; polling delay and the final-check-to-rename gap are not transactional upstream revocation. File/directory fsync is not a disaster-recovery or distributed-filesystem guarantee.

### Browser restart and recovery behavior

After normal restart, healthy durable disk becomes authoritative **without Restore from this browser**, once a fresh owner snapshot is available. All browsers may be offline during restart. Initialized-empty remains authoritative. Old process CAS fails instead of silently rebasing.

Unsaved drafts and conflicting legacy browser copies remain for explicit review; they are not automatically uploaded or overwritten. Explicit legacy browser restoration is allowed only into an **absent** collection with current ownership/CAS, not over an initialized-empty or existing durable collection. There is no equivalent MCP import. Missing/corrupt configured disk is a startup failure, not permission to adopt a browser copy.

Browser copies contain validated authored data/bookkeeping (including durable StoreID where applicable), never policy/tokens/access decisions. They are **not the authoritative backup** of durable storage. An uncertain result must not overwrite recovery copies. Read back confirmed state, compare privately and deliberately resolve conflicts; no auto replay/merge, offline queue, newest-copy guarantee or distributed synchronization is provided. NPM browser choices, logo and SSH-account preferences remain browser-local.

## Offline backup, restore and downgrade

No backup or provisioning is performed by the application or these docs. Before first durable/v4 write, obtain a separate owner checkpoint recording exact host/container paths, runtime UID/GID, modes and ACLs, private backup location/retention, expected schema/namespace, verified image digest and rollback procedure. Never loosen `/mnt/personal/docker-configs/velociportal` or its existing `950:950`, directory `0750`, file `0640` permissions. The private 0700/0600 directory is independently provisioned, not a repair of that protected directory.

1. **Stop the sole writer/deployment before operator edits, backup restoration or source replacement.** Keep browser drafts and possible intended-but-unacknowledged changes for private comparison. Do not delete locks under a running process or assume a timeout means no disk change.
2. **Back up complete documents privately**, preserving owner, mode, namespace and StoreID, alongside compatible configuration and image records. Protect private URLs/ownership from public logs/repositories; browser copies do not replace this backup. Shared/private files are independent commit domains: record the backup point for each, never claim a cross-file atomic snapshot.
3. **Reconcile actual disk before resuming after uncertainty.** Compare the stopped file with reviewed backup/intended change. Restore or retain only a complete validated document at the same approved path/owner/modes. Missing/corrupt/wrong-bound files must be corrected through that explicitly approved offline procedure, not chmod/chown, automatic migration or fallback. Validated locked startup rereads/syncs; fresh owner evidence still gates private access.
4. **Treat backup rollback as possible data loss.** It can lower persisted collection revisions or reintroduce old authored rows. Ordinary same-store restore retains its namespace/StoreID; the new process generation rejects old in-flight CAS. Preserve conflicting browser copies/drafts for review instead of automatically importing them or claiming newest recovery. Restore tests belong in a separately approved isolated environment; fsync alone is not backup validation.
5. **Source replacement is not an ordinary restart.** Changing tailnet/control-plane source or NPM database requires stop, deliberate namespace rotation and explicit offline mapping reconciliation. Check NPM IDs and typed Machine IDs against the new source; never copy by display name. Namespace-only editing cannot migrate application digests because their preimages are absent: recreate defaults from current authorized owners' entries or omit them in a reviewed replacement. Private login/User-ID reconciliation is a separate reviewed operation, never automatic adoption, account merge or deletion. Do not bypass mismatches by blindly rewriting binding fields.
6. **Prepare compatibility before image downgrade.** Older RC.18/19 readers cannot read v4 or provide durable personal/MCP behavior. Use a separately reviewed compatible metadata backup/document, disable editing, remove unsupported MCP/private-file/namespace settings as appropriate for the exact older binary, and preserve the durable private snapshot offline with its compatible configuration. Removing only the file knob deliberately selects volatile mode and does not migrate disk data to RAM. Do not lower a JSON version while retaining new fields, downgrade only the image, delete the private file, or promise lossless recovery through browser storage. RC.16 additionally requires v1/v2 metadata and removal of shared editor settings. Retain the forward-compatible backups and immutable image records before proceeding.

See the [MCP release/live worksheet](mcp.md#release-and-live-acceptance-worksheet) for pending native-client, genuine identity, browser, restart and permission-removal evidence. Temporary-file tests and static review are not installed-storage recovery acceptance.
