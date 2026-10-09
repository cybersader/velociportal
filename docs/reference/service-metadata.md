# Service metadata and shared editing

**Shared editing and metadata v3 are unreleased. Published RC.16 remains the read-only v1/v2 baseline.** This feature changes shared presentation for services a viewer can already see; it does not change policy, NPM, DNS, backend targets, SSH evidence or access. No deployment, provisioning or migration is performed by this documentation.

## One file, policy first

`SERVICE_METADATA_FILE` is the only presentation configuration source. Entries associate with existing positive NPM `proxy_host_id` values **after** supported policy matching. Metadata cannot create, hide or enable cards, repair a domainless host, or alter health. A concrete NPM domain remains the preferred default; a wildcard-only card remains visible without a usable link until an explicit URL is supplied.

The strict reader accepts:

| Version | Fields in each service entry |
|---|---|
| 1 | `proxy_host_id`, optional `name` and `url` |
| 2 | v1 plus optional `category` and `order` |
| 3 (unreleased) | v2 plus optional finite `icon` ID |

All documents require exact `version` and `services` keys. Unknown, duplicate, case-variant, malformed and trailing JSON is rejected. The complete file is bounded to 256 KiB and 1,024 entries. Names are canonical unpadded strings up to 120 characters; URLs are bounded to 2,048 bytes and must be validated absolute HTTP(S) URLs without credentials or wildcard hosts. Categories are canonical strings up to 64 characters; order is an integer from 0 through 1,000,000. Existing v1/v2 `name`/`url` null presence remains readable and is preserved for untouched entries. Category and order require v2 or v3; icons require v3.

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

Categories and order continue to group/sort only authorized cards, with uncategorized last and deterministic name/ID fallbacks. The dashboard edits only **name, URL and icon**. Empty fields remove those overrides. **Reset name/link/icon** preserves category/order and unrelated entries; an entry disappears only when no fields remain.

Startup does not write or migrate the metadata document. The first successful explicit save serializes the **complete preserved document as deterministic v3**, including entries the editor cannot currently see. Subsequent saves do not downgrade it. `suggest-hostnames` still emits only a strict v1 **proposal fragment**; never replace a complete v2/v3 file with that fragment. Preserve category/order/icons and unrelated entries when reviewing an operator merge.

## Optional exact-login editors

Both settings absent leave existing read-only behavior. Partial configuration fails. Both present require an existing valid `SERVICE_METADATA_FILE` in safe, separately provisioned writable storage.

| Setting | Contract |
|---|---|
| `PORTAL_EDITORS` | Strict JSON array, at most 32 exact full logins, each at most 254 bytes; no padding, control characters, duplicates, aliases, case folding or inferred roles |
| `PORTAL_PUBLIC_ORIGIN` | Exact canonical browser-facing HTTP(S) origin, including non-default port when used; no path, query, fragment, userinfo, wildcard or trailing slash |

An empty editor array configures storage but grants nobody editing. Disable the feature by **removing both settings**, not by setting them to empty strings. Current tailnet HTTP Serve remains supported; no HTTPS infrastructure change is implied.

The narrow setup subcommand configures these two settings only, preserving other environment entries:

```bash
velociportal setup service-editor --env-file velociportal.env --editors '["alice@example.com"]' --origin http://portal.tailnet.ts.net:8081
```

`SERVICE_METADATA_FILE` must already be set in that file; when Compose normally supplies it, use the corresponding fixed runtime path in the operator's configuration too. Setup validates syntax but does not provision or probe runtime metadata storage. Logins in command arguments can be visible to trusted same-host administration tools and shell history; use the authenticated private environment-file editing path when that is unsuitable. To remove both settings without changing metadata:

```bash
velociportal setup service-editor --env-file velociportal.env --disable
```

Doctor reports only coarse editor enablement and read-only storage prerequisites. It creates no lock/probe files, performs no editor write test, and prints no editor identities/origin or saved URLs in its editor diagnostic. Passing does not prove lifetime-lock availability, rename durability or a successful save. Normal Doctor upstream checks remain separate and networked.

## Editor interaction and boundary

Only configured exact logins receive **Edit service** buttons, including wildcard cards. The main service link remains useful without JavaScript. A native dialog becomes a scrollable mobile sheet; fields have explicit labels, a finite local icon picker/preview, **Save for everyone**, Cancel, and a confirmed reset. Empty fields use current NPM defaults.

Drafts survive harmless polling and search filtering, but are cleared on identity change or loss of editing/service visibility. Search-hidden is not unauthorized. Conflicts keep the draft and disable saving until explicit reload/review; no per-field merge or silent overwrite occurs. Save is announced only after server confirmation. The editor's authorized fragment refreshes with search preserved; other viewers update on ordinary polling/reload. Drafts, form tokens and search are memory-only, never browser storage or URL parameters. Browser-local logo and SSH-account suggestions are separate unchanged preferences.

The API requires trusted-proxy identity, unique raw identity headers, exact configured login/Host, and a currently visible positive ID. POST additionally requires exact Origin, a 30-minute identity/origin-bound HMAC CSRF token, acceptable Fetch Metadata, exact JSON content type, and a strict body no larger than 16 KiB. Missing and invisible IDs are indistinguishable. Responses are no-store/frame-denied with no CORS. Fresh complete authorization is checked immediately before save using the existing three-poll-interval cutoff; this is **not transactional upstream-revocation enforcement**. Metadata never mutates the authorization cache.

## Finite embedded icons

Available IDs are `generic`, `adguard-home`, `gitea`, `grafana`, `home-assistant`, `immich`, `jellyfin`, `nextcloud`, `nginx-proxy-manager`, `paperless-ngx`, `portainer`, `qbittorrent`, and `uptime-kuma`. An empty field restores the generic default. Selection is explicit, never inferred from a private hostname. There are no uploads, arbitrary paths, external icon URLs, favicon fetches or runtime logo-service calls.

The twelve unmodified PNGs come from [Dashboard Icons revision 57e939e](https://github.com/homarr-labs/dashboard-icons/tree/57e939e504eda0ea764098015da93aa666ad6f31), bounded, decoded and visually reviewed before embedding. `assets/service-icons/provenance.json` records exact upstream paths, sizes and SHA-256 digests; `LICENSE` and `ATTRIBUTION.txt` retain Apache-2.0 licensing/attribution. The generic SVG is an original inert project asset. Brand trademarks remain their owners' property; repository licensing grants no trademark rights, affiliation or endorsement. Arbitrary logos, access history and account-synchronized personalization remain deferred.

## Storage, conflicts and recovery

One process owns the single file and a stable adjacent lifetime lock. Safe storage requires a regular non-symlink, single-link target owned by the runtime effective UID with owner read/write and no group/other write; the dedicated directory must also be runtime-owned, owner-rwx, and not group/other writable. Ancestors cannot be symlinks or non-sticky group/other-writable directories. No chmod/chown/ACL repair is attempted. Do not replace or unlink a live lock inode.

Saves use whole-file byte revisions: **any** intervening edit, even to an unrelated service, conflicts. A separate opaque form revision detects changed NPM targets/defaults but ignores ordinary poll timestamps. No fingerprint/generation/tombstone is persisted. Positive IDs are not stable service identities: when deleting or reusing an NPM ID, review and remove obsolete metadata. A reused ID can receive old presentation, never new authorization.

A save creates an exclusive adjacent `0600` temporary file, writes and fsyncs it, rechecks disk/current visibility/target, renames, fsyncs the directory, then publishes immutable confirmed memory. Before-rename failures preserve the original file. If directory fsync fails **after rename**, the disk outcome is explicitly uncertain, writes are disabled, and rendering keeps the last confirmed memory until restart/reconciliation. Do not claim the old file was restored, retry blindly or delete synchronization files.

Live external editing is unsupported while the dashboard writer runs. Observed drift causes conflict; byte checks are **not race-proof against an uncooperative external writer**. For operator changes, stop the writer, privately review/merge the complete file, preserve every supported field and storage ownership/modes, then restart. Startup validates and syncs current disk before enabling editing, without document migration. For uncertain saves, stop, compare/reconcile actual disk against the reviewed private backup and intended save, and restart only with a known valid complete document.

**Before the first v3 save**, establish a separately approved private metadata backup/restore and compatibility rollback procedure. RC.16 is v1/v2-only: rollback requires reviewed compatible metadata **and editing-disabled configuration**, not merely its image digest. An icon-bearing v3 file cannot be assumed readable by RC.16. Do not downgrade by changing the version number while retaining v3 fields. Restore a reviewed v1/v2 backup or deliberately produce a compatible document privately, accepting/reviewing the presentation changes. No backup, file transfer or production migration has been performed here.

For mount alternatives and the unchanged non-root/private-port deployment boundary, see [TrueNAS shared editing](../guides/truenas-scale.md#optional-shared-service-editing-unreleased).
