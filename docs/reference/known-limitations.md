# Known Limitations

This page describes the current implementation and approved deployment boundary, not the long-term design.

## Policy support

- **Legacy ACL rules only.** Velociportal evaluates `acls` entries with `action: "accept"`. Grants, SSH, posture, application capabilities, and other newer policy constructs are not evaluated.
- **Ports and protocols are ignored for visibility.** Destination ports are stripped and protocol fields are not modeled. A card may appear even when the real rule permits only another port or protocol. Headscale remains the enforcement boundary.
- **`autogroup:internet` fails closed.** It is not treated as a wildcard.
- **No human source-tag inference.** `tagOwners` and tags on owned nodes do not make a human a `tag:*` source. Tags resolve destinations only.
- **Empty or unsupported policy means no cards.** Velociportal requires a supported matching ACL rule even if Headscale has broader defaults.

## Service matching

- **The join uses NPM `forward_host`.** Supported destinations include exact hosts/IPs, CIDRs, policy aliases, destination tags resolved to node IPs, `*`, and `autogroup:self`.
- **The join is not proven against a real deployment.** NPM may store a Docker name while Headscale resolves an IP or tag. A matching RFC1918 address also requires an advertised, approved, and client-accepted subnet route.
- **NPM access lists are not visibility inputs.** `access_list_id` is not used to decide cards.
- **Card URLs reuse NPM's backend scheme.** An HTTPS frontend with an HTTP backend can produce an incorrect `http://` card. Validate every link.
- **Only the first NPM domain becomes the card.** Additional domains are not rendered separately.

## Runtime upstream transport

- **Headscale HTTP is narrowly allowlisted.** Configuration accepts HTTP only for exact local/internal host forms implemented by the validator. The canonical production value is `http://headscale.velociportal.internal:8080`. Other locations require verified HTTPS.
- **The allowlist does not prove confinement.** It cannot prove that `velociportal-upstreams` exists, that the alias identifies the intended container, that port `8080` is not published elsewhere, that direct routing is absent, or that untrusted containers cannot join the bridge. Those are deployment acceptance requirements.
- **NPM HTTP is narrowly allowlisted and topology-dependent.** Canonical runtime NPM traffic uses `http://npm.velociportal.internal:81`; only that exact alias and same-host/loopback compatibility routes may use HTTP. Every other NPM location requires verified HTTPS. The hostname check still cannot prove the deployed route is private.
- **Credentialed clients refuse redirects and environment proxies.** Both transports bound response headers/bodies. HTTPS uses normal verification and TLS 1.2 or newer. There is no insecure TLS mode.
- **The private bridge is egress-capable by design.** TrueNAS catalog apps replace their implicit default network when any UI-managed network is selected, so Headscale and NPM require `velociportal-upstreams` to provide outbound NAT and Docker DNS. A normal user-defined bridge does not publish ports to the LAN, but host or privileged workloads remain within the deployment trust boundary.
- **The base production stack has no CA mount.** The optional private-CA overlay mounts only a readable public root for verified-HTTPS alternatives. Never mount a CA private key or leaf private key.

## NPM Headscale control proxy

- **NPM is a trust and availability boundary.** Existing NPM terminates the trusted Headscale HTTPS origin required by brand-new clients and HTTPS-only `headscale-ops`, then forwards to privately addressed Headscale HTTP.
- **NPM can observe control traffic and operator Bearer API keys.** Keep operator and Velociportal runtime keys separate. Runtime Velociportal bypasses NPM.
- **Protocol preservation requires live proof.** WebSocket and HTTP upgrade behavior must be enabled and tested against the real Headscale version.
- **Logging posture requires live proof.** Custom NPM logs must not record `Authorization` or full request headers.
- **NPM backup/restore is part of Headscale availability.** Database, configuration, certificates, and automated renewal state require tested backups.
- **Pre-tailnet certificate trust is mandatory.** The privacy-preserving canonical form uses split-horizon/private DNS plus an existing publicly trusted wildcard certificate from the operator's automated DNS-01 NPM lifecycle. Do not publish the Headscale hostname/address in public DNS or issue an exact-host public leaf certificate that exposes it in certificate-transparency logs. If a required joining client does not already trust it, stop rather than disabling verification.

## Identity and browser ingress

- **Only `Tailscale-User-*` headers are supported.** `Tailscale-User-Login` is required. Authentik, Authelia, `Remote-User`, and `X-Webauth-*` are not accepted.
- **Full-domain identities match exactly.** A trusted `alice@example.com` login does not inherit ambiguous `alice@` or bare `alice` membership.
- **The immediate source must be trusted.** Requests outside `TRUSTED_PROXY_CIDR` are rejected. Broad Docker, LAN, or tailnet CIDRs weaken spoofing resistance.
- **Host-loopback topology requires Docker Engine 28+.** Older engines may expose localhost-published ports to same-L2 hosts. Host processes and host-network containers that can reach loopback may share the trusted Docker-gateway source.
- **Human Serve identity only.** Tagged-device and Funnel requests do not receive user identity headers.
- **Tailnet HTTP Serve is canonical.** The browser origin is HTTP on port `8081`, but ordinary on-path LAN/router/ISP observers see WireGuard-protected transport. Endpoint, host, NPM, Tailscale/Headscale control-plane, and trusted-workload compromise remain in scope.
- **Headscale automatic HTTPS Serve remains future work.** Official Tailscale can automate `*.ts.net` certificates. Track Headscale [issue #2527](https://github.com/juanfont/headscale/issues/2527) and [PR #3300](https://github.com/juanfont/headscale/pull/3300). Tailnet HTTP Serve is not a release blocker.
- **NPM is not portal identity.** An NPM route cannot derive the caller's human Tailscale identity.

## Deployment and operations

- **Production requires Docker Compose 2.33.1+ and Docker Engine 28+.** Raw env-file parsing, explicit network gateway priority, and safe localhost publication depend on those versions.
- **Production always consults the registry.** `pull_policy: always` prevents silent reuse of a tagged local image, but operators must still record the resolved digest and never use `latest`.
- **Production and repository projects must remain distinct.** Use a production project name other than the repository workflow's `velociportal` project.
- **No source build or recurring NAS shell is canonical.** The one normal Headscale app-shell action is the first short-lived API-key bootstrap. Routine administration is workstation-driven through HTTPS-only `headscale-ops`.
- **No CA or durable application state belongs on the router.** Router replacement restores ordinary DNS and routing only. Durable Headscale, NPM, policy, Serve, network, and Velociportal state stays on TrueNAS and backups.

## Validation status

Automated tests and fixtures do not prove the real NPM control path, Docker confinement, certificate lifecycle, identity injection, restart recovery, backup restore, join correctness, links, or per-user reachability. RC.1 failed the first live TrueNAS network-attachment gate and was rolled back safely; RC.2 corrects that topology but has not yet passed end-to-end TrueNAS acceptance.

No public support claim is warranted until at least two real identities, trusted pre-tailnet NPM HTTPS, separate keys, WebSocket/upgrade behavior, authorization-header logging posture, LAN-negative Headscale/Velociportal ports, restart persistence, backup/restore, every NPM join, every card link, and actual Headscale reachability have been checked. Use the [real-deployment worksheet](../getting-started/validation.md).
