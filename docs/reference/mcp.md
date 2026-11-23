# Service-hosted MCP (RC.20 candidate)

The current source adds an **opt-in `/mcp` endpoint in the existing Velociportal service**, for finite icons, the caller's durable personal entries, and shared presentation icons and default card order on already-authorized cards. No workstation hosts a second server. There is no model, upstream administration, login system, token issuer, database, or new listener.

!!! warning "RC.20 candidate, not verified deployment or universal client support"
    `v0.2.0-rc.20` is the selected prerelease candidate for MCP, durable personal storage and metadata v4. Immutable RC.18/19 do not contain these features; RC.19 fixes the earlier route panic. This preparation does not assert an RC.20 image/digest has been published. Use these opt-ins only with a verified compatible immutable image and separately approved storage/deployment. On 2026-11-14 the owner authorized prerelease publication with the remaining native-client and live-acceptance gaps disclosed, rather than blocking publication. This is not a compatibility, security, deployment or support certification; it authorizes no live NAS, storage, permissions, network or Serve changes. Tailscale remains preview.

## Client status and endpoint boundary

Claude Code 2.1.321 passed 12 native HTTP tool calls across two fresh processes (14 internal turns) against the production handler on localhost with synthetic identity: finite icons, durable personal create/read/delete, NPM shared-icon set/reset, and reconnect/read of initialized-empty state. This was not a server restart or genuine Serve test. `personal_update` and Machine/Application native target or mutation calls were not exercised. Native Codex CLI was unavailable; native T3 had no supported temporary enrollment path and remains unverified. Genuine Serve/two-device identity, native browser, live restart/recovery, LAN-negative and the full provider acceptance matrix remain unverified. Compatibility with one CLI does not establish another client's integration. T3 still needs its actual connection path tested, including whether it connects itself or through a provider integration.

The intended endpoint is the **existing portal origin plus `/mcp`**, for example the illustrative `http://portal.tailnet.ts.net:8081/mcp`. This is not a deployed address or working client setup. The canonical path remains tailnet-only HTTP Serve `:8081 -> http://127.0.0.1:18080`; WireGuard protects ordinary on-path traffic. Plain HTTP is not HTTPS or a browser secure context. Endpoint/control-plane/host compromise remains in scope.

A client must support this non-localhost plain-HTTP path, stateless JSON responses, the protocol/header rules below, and genuine human Serve identity. A cloud connector, tagged machine, unsupported OAuth requirement, HTTPS-only client restriction, or unsupported transport is an incompatibility to report—not permission to add tokens, a trust-wrapper bypass, identity relay, workstation server, public tunnel, SSO, or new Serve/TLS infrastructure. No client installation/enrollment command is offered as verified setup.

## Opt-in configuration

| Variable | Exact source behavior |
|---|---|
| `PORTAL_MCP_ORIGIN` | Absent disables `/mcp` (404). Supplied empty/invalid fails configuration. Canonical HTTP(S) origin only; must equal every enabled shared/personal origin. |
| `PORTAL_PERSONAL_SERVICES_ORIGIN` | Independent Tailscale-only personal catalog opt-in; absent disables. Must be the existing portal origin, not an application URL. |
| `PORTAL_PERSONAL_SERVICES_FILE` | Absent retains browser-managed volatile RAM mode. Present requires Tailscale, personal origin, valid namespace and preexisting valid private storage. Personal MCP tools require this durable mode. |
| `PORTAL_STATE_NAMESPACE` | Canonical `[a-z0-9][a-z0-9_-]{0,63}` source binding. Required for durable storage and every v4 reader/writer; supplied empty/invalid always fails. Requires a metadata file or private file consumer, not merely MCP. |
| `PORTAL_EDIT_MODE=shared`, `PORTAL_EDITORS`, `PORTAL_PUBLIC_ORIGIN`, `SERVICE_METADATA_FILE` | Existing shared-editor prerequisites remain necessary for shared target/icon tools; add a valid namespace. Exact editor allowlist, not automatic administrator-role permission. |
| `PORTAL_CUSTOM_ICONS` | **Unreleased source only.** Absent keeps everything below unchanged. Exactly `enabled` adds three [custom PNG logo tools](#opt-in-custom-png-logo-tools-unreleased-source) and requires shared editing, `PORTAL_MCP_ORIGIN` and a namespace. Writes metadata v5. |

MCP alone creates neither a personal store nor a shared writer/lock and does not change edit mode. Readonly/off/browser metadata can render v4 defaults without enabling shared tools. RAM personal entries can supply the editor's **own active application target** for shared icon operations, but cannot be read or mutated through personal MCP tools. Headscale has finite icons and eligible NPM shared targets only; it gains no personal or SSH Machine support. The generic shared tool schema may still list `machine`/`application` kinds: Headscale Machine listing is empty; Application without a personal store is unavailable as `not_found`.

Use [storage and recovery](service-metadata.md#durable-personal-storage-unreleased-source) and the [opt-in deployment checkpoint](../guides/truenas-scale.md#unreleased-source-private-storage-and-mcp) before configuring files. All values remain disabled/commented in the examples. Production provider env files retain `go-quoted-v1` values through Compose raw env-file forwarding; origin/file/namespace values are quoted there, not shell-evaluated.

## Identity on every request

`IdentityMiddleware` accepts `Tailscale-User-*` only from `TRUSTED_PROXY_CIDR`. MCP additionally requires an exact canonical unpadded full human login, consistent singleton identity headers, and exact configured Host. Initialization, `clientInfo`, arguments, prior discovery, handles and RPC IDs never select an identity or retain permission. Each operation rechecks the actual request, current configuration, fresh complete snapshot and current target/owner, including at the final pre-rename checkpoint.

**Human-device authority is shared by processes on that device.** It is not independent agent authentication or a sandbox against another process using the same human's network authority. Tagged/no-human-identity, Funnel and cloud-connector impersonation are unsupported. The host-loopback trusted-workload boundary also remains: host processes that can reach the raw loopback socket may share the trusted source. Do not widen the trusted CIDR or expose that socket.

| Admission | Accepted headers |
|---|---|
| Native | `Origin` and all Fetch Metadata absent |
| Same-origin browser-shaped request | Exactly one matching `Origin`; any supplied `Sec-Fetch-Site` must be `same-origin`, `Sec-Fetch-Mode` must be `cors` or `same-origin`, and `Sec-Fetch-Dest` must be `empty` |
| Always rejected | `Sec-Fetch-User`; empty/null/duplicate/cross-origin Origin; Fetch Metadata without Origin or contradictory values; `Authorization`, `Cookie`, `Mcp-Session-Id`, preflight request headers—even empty |

There is no permissive CORS or alternative bearer/cookie/session authentication. Browser personal/shared APIs keep their own exact Origin/Host and CSRF rules; MCP is a separate guarded transport, not a CSRF bypass to browser endpoints.

## Wire contract and resource bounds

- Exact `/mcp`, **POST only** after admission. Enabled unsupported methods return 405; no GET event stream. Query/ForceQuery, encoded paths and trailing-slash aliases are unsupported.
- One JSON-RPC 2.0 object; no batches. `Content-Type: application/json` (optional UTF-8 charset), and one `Accept` header advertising **both** `application/json` and `text/event-stream` without media parameters. Responses choose JSON, not SSE.
- Only protocol **`2025-11-25`** is implemented. `initialize` may omit `MCP-Protocol-Version`; supplied versions must match. Initialization returns the supported version, which the client must accept before proceeding. Subsequent calls require the matching header. The historical `serverInfo` name `velociportal-readonly-prototype` and version `0.1.0` are compatibility labels, **not the application release version or a statement that writes are impossible**.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`. Valid admitted notifications return 202 without executing application reads/mutations; notification-shaped tool calls are not saves. No sessions, legacy SSE, server push, tasks, sampling, subscriptions or bulk import.
- Eight immediately admitted MCP requests; excess returns 503, not an unbounded queue. Personal reads/mutations also acquire one of the **same eight personal slots shared with browser requests**, not eight additional personal slots. Slots remain held through synchronous dispatch/commit.
- **16 KiB complete request**, JSON depth **16**, route-local read/write deadline **10 seconds**, and **256 KiB complete response**, including RPC envelope/ID and both `structuredContent` and text representations. No truncated success. JSON rejects unknown, duplicate, case-variant and null fields where not explicitly permitted; tool objects have exact allowed keys. Encoding failure after a successful mutation is uncertain, not rollback.
- Identity-derived responses are `no-store`. Tool arguments, private URLs, ownership IDs and result bodies must not be logged. Client-side transcripts may contain the caller's authored data: treat them as private too.

## The ten fixed tools

Discovery is recomputed per request and may advertise fewer tools when storage/ownership/freshness/editor capability is unavailable. Dispatch repeats authority checks even if the client retained a descriptor.

| Tool | Arguments | Result / authority |
|---|---|---|
| `icons_list` | Empty object | Finite embedded icon IDs and labels; no private catalog or network fetch |
| `personal_read` | Empty object | Own confirmed durable state and coarse per-entry projection; fresh exact-login + Users API ID ownership |
| `personal_create` | `store_id`, `generation`, `revision`, `name`, `url`; optional `icon` | Own confirmed state after create; omitted icon means inherit |
| `personal_update` | Create fields plus own `id` | Replace that own entry's authored fields; omitted icon clears the authored override to inherit |
| `personal_delete` | `store_id`, `generation`, `revision`, own `id` | Own confirmed state after delete; initialized-empty remains authoritative |
| `shared_targets_list` | `kind`; optional `cursor`, `limit` | Current caller-visible targets of one kind: `npm`, `machine`, or `application` |
| `shared_target_get` | `target` exactly `{kind, handle}` | Selected name/shared/effective icon, NPM `category`/`order` when set, and opaque `revision`, `target_revision` |
| `shared_icon_set` | `target`, `revision`, `target_revision`, finite nonempty `icon` | `{"status":"saved"}` only after confirmed shared commit |
| `shared_icon_reset` | `target`, `revision`, `target_revision` | Remove only selected shared icon/default; same confirmed-save result |
| `shared_order_set` | `revision`, `entries` (1–64 distinct NPM targets) | Set or remove shared default `category`/`order` in one commit; `{"status":"saved","revision":"…"}` |

Personal results contain own authored document, `durable`, stable `store_id`, process `generation`, collection `revision`, initialized state and coarse projection—not a whole private file, browser CSRF token, foreign collection or policy snapshot. There is no MCP restore/import tool. No tool accepts an arbitrary shared destination URL/path/digest/provider-ID tuple. Icons are finite embedded IDs, never uploads, external logos or filesystem paths.

Shared target selection uses actual current card evidence: policy-matched NPM service; full supported SSH + independent Grant TCP/22 + current device capability for a Machine; or the maintainer's **own active personal entry** for an application. It never enumerates foreign personal records, the global digest map, matching-user counts or backend topology. No icon creates a card or supplies authorization.

### Shared default card order

`shared_targets_list` and `shared_target_get` include an NPM target's current shared `category` (string) and `order` (integer 0–1,000,000) when the shared file sets them; absent fields are omitted. Machine and application summaries never carry them.

`shared_order_set` takes the shared document `revision` from either read tool and `entries`, an array of 1–64 objects. Each entry has exactly `target` plus at least one of `order` and `category`:

```json
{"revision": "…", "entries": [
  {"target": {"kind": "npm", "handle": "…"}, "order": 10, "category": "Media"},
  {"target": {"kind": "npm", "handle": "…"}, "order": null}
]}
```

- A value sets the field, `null` removes it, and an omitted field is left unchanged. A touched entry left with no fields is removed from the file.
- Every target must be a distinct, currently visible NPM service for the caller. Duplicate targets are invalid arguments. A machine, application, missing or invisible target returns the unavailable-target response (`not_found`) and nothing is written.
- All entries are applied in **one** commit with whole-document CAS, or none are. Every target is rechecked immediately before the rename. A stale `revision` returns `conflict`. Setting values that are already current returns the current revision without writing.
- The result carries the new document `revision`, so a client can chain another call without re-reading. Lost or uncertain responses still require read-back, not replay.
- The writer is the ordinary lossless NPM writer: a legacy file becomes v3, v4 stays v4 and v5 stays v5. Name, link, icon, custom icon, other entries, machine/application arrays, binding and null presence are preserved.
- It requires the same shared-editor capability as the icon tools. It changes presentation order only: it never creates, hides or authorizes a card.
- Sixty-four entries with 64-character ASCII categories fit the 16 KiB request limit; long non-ASCII categories may need smaller batches.

This sets the **shared default** order everyone sees. A personal order saved in a browser (**Account settings → Edit card order**) still applies on top of it in that browser.

### Opt-in custom PNG logo tools (unreleased source) { #opt-in-custom-png-logo-tools-unreleased-source }

With `PORTAL_CUSTOM_ICONS=enabled`, discovery appends three tools after the shared tools, under the same shared-editor capability. With it absent, discovery and calls are unchanged and these names are unknown tools (`-32602`).

| Tool | Arguments | Result / authority |
|---|---|---|
| `custom_icon_import` | `png_base64` (standard padded base64, at most 10,924 characters), `revision` | `{"status":"saved","custom_icon":"sha256:…"}`. Stores one canonical re-encoded PNG. Identical images return the same ID without writing. Assigns nothing. |
| `custom_icon_get` | `target` exactly `{kind, handle}`; NPM only | Name, current `custom_icon` (empty when none), opaque `revision` and `target_revision` |
| `custom_icon_set` | `target`, `revision`, `target_revision`, `custom_icon` (stored ID, or empty to clear) | `{"status":"saved"}` after a confirmed shared commit; an unchanged assignment does not write |

Machine and application targets return the unavailable-target response. There is no listing of stored images, gallery, delete or cleanup tool. The image renders only inside markup for authorized cards. Formats, limits, versioning and downgrade: [custom PNG logos](service-metadata.md#custom-png-logos-and-metadata-v5-unreleased-source).

### Pages and optimistic concurrency

Target pages contain at most **64** entries (default 64; supplied integer 1–64), may be shorter to meet the full response byte budget, and have no total count. Handles are caller/provider/namespace/kind/process bound. The opaque cursor is also caller/kind/source/process bound (current encoding 87 bytes, bounded at 256); every page recomputes current authorization. It is not a frozen catalog: if the anchor disappears, the call conflicts and the client must restart listing; new earlier-sorting handles may require a fresh listing to see them.

Shared saves compare the complete shared-file byte revision and current target revision. Any intervening shared save can conflict, even in another target kind. Personal mutations compare stable store ID, fresh process generation and own collection revision; no whole-private-file revision discloses other users' activity. Never refresh CAS silently, auto-merge, retry a mutation or treat the JSON-RPC ID as an idempotency key.

### Failure and recovery

Invalid arguments or unknown tool names return protocol `-32602` (`Invalid params`). Operation failures return coarse `isError:true` results with `error` such as `forbidden`, `not_found`, `unavailable`, `conflict`, `uncertain`, `capacity`, or `invalid_request`, without parser/filesystem detail. Missing/invisible/foreign/inactive/unsupported-ID targets share the unavailable-target response; current authority is established before exposing a CAS conflict.

A timeout, cancellation, disconnected response or uncertain encoding after commit does **not** mean rollback. Read back confirmed state and review; never replay automatically. If private storage is uncertain, both adapters suppress authoritative private reads and writes until validated locked reopen/reconciliation. Shared uncertainty disables shared mutations and editable read-back; older confirmed presentation may still decorate visible cards but is not proof a save failed. These failure domains are independent: private failure does not disable NPM/Machine shared operations. See [offline recovery and downgrade](service-metadata.md#offline-backup-restore-and-downgrade).

Current ownership/visibility comes from the latest complete snapshot, with the three-poll-interval freshness cutoff. Polling delay and the final-check-to-rename interval remain a bounded stale-evidence/revocation window, not transactional upstream revocation or an enforcement guarantee. Network/config changes and filesystem sync do not erase that window.

## Remaining client and live acceptance worksheet { #release-and-live-acceptance-worksheet }

These checks remain **unverified acceptance requirements**, not completed product tests. The owner waived them as prepublication blockers for RC.20 only; no live work is authorized by that waiver. Retain the complete [TrueNAS provider worksheet](../guides/truenas-scale.md#11-run-required-acceptance) and [real-deployment validation](../getting-started/validation.md). The scoped localhost native-Claude result above does not complete this worksheet.

- [ ] Record a future published immutable image digest containing this exact source feature set; retain RC.18/19 unchanged. Record runtime UID/GID, exact independent directories/files, modes, source namespace, backup and downgrade plan before owner approval.
- [ ] For installed native Claude Code, Codex CLI and T3 Code, record exact versions and actual connector origin. Prove initialize, discovery, real bounded tool call, reconnect and JSON responses on the intended transport—not a direct HTTP substitute for a native call.
- [ ] Use two human-owned Tailscale machines with no workstation acting as server. Prove same-human authored state across devices and a second distinct human's isolation. Prove genuine Serve header injection/replacement and reject tagged/no-human, alternate-auth and direct raw-source requests. Raw port must be unreachable from the LAN.
- [ ] Compare current NPM, personal application and full triple-evidence Machine cards with actual HTTP/SSH reachability for both identities. Test all three icon kinds, explicit generic versus inherit, reset, browser/shared preservation, rename versus replacement and namespace/source mismatch.
- [ ] Remove editor permission, owner/direct membership or relevant Grant/SSH/capability evidence while retaining old descriptors/handles/revisions. Once current evidence reflects removal, reads/writes must fail as specified; record polling/revocation delay, not instantaneous enforcement.
- [ ] Confirm an acknowledged durable save, close **all browsers**, restart the deployment and then reconnect. Disk is authoritative after a fresh complete owner snapshot; cold/stale ownership reveals no private data, initialized-empty stays empty, old generations conflict and no browser restore is required.
- [ ] In a controlled separately approved recovery environment, exercise missing/corrupt/wrong-permission storage, wrong namespace/owner ID, conflicting browser drafts/backups, uncertain response reconciliation and private offline backup/restore. Never inject failures into production merely to complete this checklist.
- [ ] Native browser keyboard/focus/touch, unsaved drafts, conflicting legacy copies, explicit restore-only-into-absent behavior and two-device changes pass. Synthetic DOM tests are not this proof.
- [ ] Complete live OAuth refresh/revocation, authoritative role and separate owner mapping, unsupported HTTP-policy/SSH negatives, stale/cold recovery, copied-target parity, Serve restart/header replacement and LAN-negative checks before any Tailscale support promotion.

The recorded S5 suite and independent static review establish source behavior with synthetic requests/snapshots and owned temporary files. They do not certify native clients, real Serve identity, distributed synchronization, live restart, filesystem disaster recovery or production security.
