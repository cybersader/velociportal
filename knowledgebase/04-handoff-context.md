# 04 — Handoff Context

> Hot context for whoever picks this up next. Read this first when work starts.
> (Design docs 00–03 hold the stable reasoning; this holds the current state.)

## Current stage

**Concept only. No code exists.** The repo is an idea placeholder: a README
capturing the vision, a `portagenty` workspace file, and this knowledgebase. Nothing
has been built or prototyped yet.

## What's already decided (don't relitigate — see 02)

- Visibility layer **only**; complements IdPs (Authentik/Authelia), never replaces.
- Single container, `FROM scratch`, target TrueNAS Scale.
- Data sources: Headscale `/api/v1/policy` + NPM `/api/nginx/proxy-hosts`. No config DB.
- Identity via Tailscale headers; headers trusted only from the proxy path.
- Stack: **Go + templ + htmx**, static binary, stdlib-first.
- Server-side authorization before render; card-hiding is UX, not enforcement.

## What to do first when development starts

1. **Prove the two reads end-to-end** (thinnest possible spike, no UI):
   - Hit Headscale `GET /api/v1/policy` with a Bearer key; parse `groups`,
     `tagOwners`, `acls`. Confirm the shape against **your** server's
     `/api/v1/docs` — versions differ.
   - Hit NPM `POST /api/tokens` → JWT → `GET /api/nginx/proxy-hosts?expand=access_list`.
     Confirm proxy-host + access-list shapes against the running instance
     (`/api/schema` is incomplete; cross-check the web UI Audit Log).
2. **Dump both into structs** and eyeball a real dataset — the matching design (D6)
   depends on what the join key actually looks like in practice.
3. **Scaffold the cache goroutine** (`time.Ticker` + `RWMutex`/atomic pointer,
   per-request `context` timeouts) before any rendering — it's the backbone.
4. **Then** wire templ + htmx to render cards from the cache, filtered server-side.
5. Stand up the multi-stage Dockerfile early so the `scratch` image constraint
   shapes the code from the start.

## Key questions to resolve

- **ACL ↔ proxy-host join (the crux):** how do we map an NPM proxy host to an ACL
  rule? Candidates: match on `forward_host`/`domain_names`, on a Headscale tag, or
  on CIDR from the ACL `acls` destinations. Likely a **combination** — prototype
  against real data before committing (D6 open detail).
- **Headscale vs Tailscale SaaS:** primary target is self-hosted **Headscale**
  (Bearer auth). Do we also support Tailscale SaaS (Basic/OAuth, 1h token) at v1, or
  defer? Leaning defer — one target first.
- **Identity source per deployment:** Tailscale **Serve** headers vs tailscaled
  **whois** (nginx-auth pattern). Which is the documented default? May need to
  support both; pick one for v1.
- **Group resolution edge cases:** groups can't nest (Headscale), users are listed
  as `alice@`. Confirm the exact user-identifier format matches what the Tailscale
  identity header (`Tailscale-User-Login`) provides, so the join actually lands.
- **Stale tags:** Headscale has needed a reload for new node tags to take effect in
  ACL eval (issue #2389). Decide whether/how the portal surfaces staleness.
- **Metadata decoration:** icons/descriptions/categories aren't in either API. If we
  add them, where — a small mounted config file? (Keep it decoration, never a second
  permission model, per D3.)
- **License:** still TBD in the README.

## Pointers

- Vision + comparison table: `README.md`
- Why / problem statement: `knowledgebase/00-concept-source.md`
- API specifics + snippets: `knowledgebase/01-api-research.md`
- Locked decisions: `knowledgebase/02-design-decisions.md`
- Similar tools + reusable clients: `knowledgebase/03-prior-art.md`
