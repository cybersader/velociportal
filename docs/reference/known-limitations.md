# Known Limitations

This page describes the current implementation, not the long-term design.

## Policy support

- **Legacy ACL rules only.** Velociportal evaluates the policy's `acls` array. Tailscale **Grants**, SSH rules, posture rules, application capabilities, and other newer policy constructs are not evaluated.
- **Allow rules only.** Only ACL entries with `action: "accept"` can make a card visible.
- **Ports and protocols are ignored for visibility.** Destination ports are stripped before matching, and protocol fields are not modeled. A host-level match may therefore show a card even when the user's rule covers only a different port or protocol. The real ACL remains the enforcement boundary.
- **`autogroup:internet` fails closed.** It is not treated as a wildcard because internet-egress semantics do not map safely to an NPM service card.
- **No human source-tag inference.** `tagOwners` says who may assign a tag; it does not make that human user a `tag:*` source. Tags on nodes owned by a user also do not make the human identity match a source tag. Tags are used only to resolve ACL destinations to node IPs.
- **Empty or unsupported policy means no cards.** Headscale itself may have broader defaults, but Velociportal requires a supported matching ACL rule before rendering a service.

## Service matching

- **The join uses NPM `forward_host`.** Velociportal compares each proxy host's `forward_host` with supported ACL destinations, including exact hosts/IPs, CIDRs, host aliases, destination tags resolved to node IPs, `*`, and `autogroup:self`.
- **The join is not yet proven against a real Headscale + NPM deployment.** NPM often stores a Docker service name while Headscale policy destinations resolve to IPs or tags. Those values may not line up. Validate the resulting card set for at least two users before relying on it operationally.
- **NPM access lists are not visibility inputs.** `access_list_id` may appear in NPM responses, and the client contains access-list API support, but the runtime cache and matcher do not use access lists when deciding which cards to render.
- **Card URLs currently reuse NPM's backend scheme.** NPM `forward_scheme` describes the NPM-to-upstream connection, but Velociportal also uses it for the browser-facing card URL. A public HTTPS proxy host with an HTTP backend can therefore produce an incorrect `http://` card. Validate every generated link; a future fix must derive the public scheme from NPM's frontend SSL fields.
- **Only the first NPM domain becomes the card.** Additional names on the same proxy-host record are not rendered as separate cards.

## Identity and integrations

- **Only `Tailscale-User-*` identity headers are supported.** The runtime requires `Tailscale-User-Login` and optionally reads `Tailscale-User-Name` and `Tailscale-User-Profile-Pic`. It does not currently accept Authentik, Authelia, `Remote-User`, or `X-Webauth-*` headers.
- **Full-domain identities match exactly.** A trusted `alice@example.com` login does not inherit ambiguous `alice@` or bare `alice` policy membership. Legacy short/bare policy identities work only when the trusted login header itself is short/bare. Use fully qualified policy members whenever the proxy supplies fully qualified logins.
- **The source IP must be trusted.** A correct header from a source outside `TRUSTED_PROXY_CIDR` is rejected. An operator must determine the proxy address actually observed by the application; copying a broad Docker or tailnet CIDR without verifying the request path weakens the spoofing boundary. Any CIDR that Go interprets as covering an entire IPv4 or IPv6 address family is rejected, including the IPv4-mapped form `::ffff:0:0/96`.
- **Host-loopback topology trusts local host processes.** Docker NAT presents host-originated connections as the private bridge gateway. In the repository's loopback-published topology, another process with host loopback access—or a host-network container—can therefore appear to come from the same trusted `/32` as the intended host proxy. Use a dedicated private proxy-to-app container network with no host port when the Docker host runs untrusted workloads.
- **Human Serve identity only.** Tagged-device and Funnel requests do not receive user identity headers.
- **Headscale HTTPS Serve is a feature gap.** Tailscale's automatic Serve HTTPS depends on its `*.ts.net` certificate flow. Headscale tracks native HTTPS Serve support as an open feature gap. See the [TrueNAS guide](../guides/truenas-scale.md#headscale-and-tailscale-serve-https).
- **Tailscale SaaS, Caddy, Traefik, and direct IdP header adapters are planned, not implemented.** Their public pages are architectural notes only.

## Validation status

The Go implementation has race-enabled unit and integration-style tests using fixtures and `httptest`. The `validate` command can consume a live complete snapshot, explain supported joins, and compare labeled identity predictions, but it cannot prove proxy identity injection or per-user network reachability. Velociportal has **not yet been validated end-to-end against a real Headscale policy, real NPM data, and a production identity-proxy path**. Treat the first deployment as a validation exercise and complete the [real-deployment worksheet](../getting-started/validation.md).
