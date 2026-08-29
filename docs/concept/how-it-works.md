# How it works

Velociportal separates **control-plane refreshes**, **identity-aware portal requests**, and **service traffic**. Portal rendering reads one immutable in-memory snapshot and never waits on the selected control plane or NPM.

## Control plane: complete snapshot refresh

```mermaid
flowchart TD
    accTitle: Complete snapshot refresh
    accDescr: Startup or a poll tick triggers the selected provider's complete policy and node/device load plus the NPM proxy-host fetch. If every stage succeeds, Velociportal atomically replaces the snapshot. If any stage fails, it keeps the previous complete snapshot.

    Tick["Startup or poll tick"] --> Policy["Fetch selected-provider policy"]
    Policy --> Inventory["Fetch nodes or<br/>users + devices"]
    Inventory --> Hosts["Authenticate to NPM<br/>fetch proxy hosts"]
    Hosts -->|"all stages succeeded"| Swap["Atomically replace<br/>complete snapshot"]
    Policy -->|failure| Keep["Keep previous<br/>complete snapshot"]
    Inventory -->|failure| Keep
    Hosts -->|failure| Keep

    class Tick core
    class Policy,Inventory control
    class Hosts service
    class Swap accepted
    class Keep output
```

<p class="vp-diagram-note">Success and failure are written on the paths; green or neutral styling is only supplemental.</p>

- The default poll interval is `30s`.
- Each upstream call has a `10s` context timeout.
- A refresh is **all-or-nothing**: the selected provider's complete policy/inventory load and NPM proxy hosts must all succeed before publication.
- Startup performs an immediate refresh. If it fails and there is no earlier in-process snapshot, portal requests and `/healthz` remain unavailable until a later refresh succeeds.
- The cache is not persisted. A process restart always starts cold.

## Identity, control, and service sequence

```mermaid
sequenceDiagram
    accTitle: Identity, control-plane, and service request sequence
    accDescr: A background poll builds the complete snapshot from the selected control plane and NPM. A human requests the portal through Tailscale HTTP Serve over the encrypted tailnet. Serve sanitizes and injects Tailscale user headers. Velociportal checks the source, reads the snapshot, matches supported access rules, and returns filtered cards. When the human selects a card, service traffic goes through NPM to the backend without passing through Velociportal.

    participant CP as Selected control plane
    participant Catalog as NPM API (service catalog)
    participant VP as Velociportal
    participant Proxy as Tailscale HTTP Serve
    participant User as Human user
    participant Route as NPM route
    participant App as Backend service

    loop Startup and every poll interval
        VP->>CP: GET complete policy and inventory
        CP-->>VP: Supported policy + node/device metadata
        VP->>Catalog: Authenticate and GET proxy hosts
        Catalog-->>VP: Enabled service metadata
        Note over VP: Publish only after all inputs succeed
    end

    User->>Proxy: Request portal
    Note over Proxy: Remove client identity headers<br/>Inject trusted Tailscale-User-Login
    Proxy->>VP: Portal request + trusted identity
    VP->>VP: Validate source CIDR and required login
    VP->>VP: Read snapshot and match supported access rules
    VP-->>Proxy: Server-rendered filtered cards
    Proxy-->>User: Portal HTML

    User->>Route: Open selected service URL
    Route->>App: Proxy service request
    App-->>User: Service response
    Note over User,App: Service traffic does not pass through Velociportal
```

<p class="vp-diagram-note">Every participant includes its role in text. Velociportal predicts visibility; Headscale, Tailscale Serve, NPM, and the backend retain their authentication, identity, routing, and enforcement roles.</p>

## Request decision path

1. Parse the TCP source address.
2. Reject the request with `403` unless it is inside `TRUSTED_PROXY_CIDR`.
3. Require `Tailscale-User-Login`; a missing identity from a trusted source returns `401`.
4. Preserve a fully qualified login exactly. Short or bare legacy forms are accepted only when the trusted header itself uses that form.
5. Resolve supported policy groups and, for Tailscale Grants only, Users-API-authoritative role selectors for that exact login.
6. Evaluate enabled NPM proxy hosts against normalized supported access rules. Grant-derived cards require TCP to the exact backend port.
7. Sort matching cards and render HTML server-side.
8. Let embedded htmx refresh the card grid every 60 seconds without turning the app into an SPA.

[See the accepted and rejected routes →](../reference/tailscale-headers.md)

## Matching boundary

The current join compares NPM `forward_host` with supported access-rule destinations. It can resolve:

- Exact hostnames and IP addresses
- CIDRs
- Policy host aliases
- Destination tags resolved to node IPs
- `*`
- `autogroup:self`

Headscale remains legacy-ACL-only. The Tailscale preview also evaluates the narrow network-Grants subset: accepted capabilities must permit TCP to the exact NPM backend port. Exact users, defined groups, wildcard, and supported human-role autogroups may produce Grant evidence. Role membership comes only from the complete Users API response: a direct member receives `autogroup:member` plus its API role, the Owner additionally receives `autogroup:admin`, and a shared user receives none. Specialized roles do not imply one another. Lookup requires exact login equality and is never inferred from devices, owners, node tags, or `tagOwners`. Tags, IPs, CIDRs, host aliases, `autogroup:shared`, `autogroup:tagged`, and other machine sources remain non-human. Legacy ACLs and `nodeAttrs` do not consume the Users API role mapping; the known `*`/user/group/tag/`autogroup:member` attr-only `nodeAttrs` targets using `funnel` remain non-authorization metadata. NPM access lists, posture, routing, services, IP sets, application capabilities, and unknown semantics are not modeled. `autogroup:internet` fails closed.

!!! warning "Validate the join on real data"
    NPM may store a Docker DNS name such as `grafana`, while Headscale destinations resolve to an IP address or tag. The current join is covered by fixtures but has not been proven end-to-end. See [Known Limitations](../reference/known-limitations.md).

## Health endpoint

`GET /healthz` returns:

- **`200`** when a complete snapshot exists and is no older than three poll intervals.
- **`503`** when the cache is empty or stale.

A successful health response means Velociportal has a recent complete snapshot. It does not prove that every rendered card is reachable, that each generated URL uses the correct public scheme, or that the matcher reflects unsupported policy features.
