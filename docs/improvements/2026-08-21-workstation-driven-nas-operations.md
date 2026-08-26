---
date: 2026-08-21
type: improvement-request
target: Headscale/Velociportal operations workflow
priority: high
status: completed
tags:
  - operations
  - automation
  - least-privilege
  - truenas
  - headscale
  - velociportal
source: direct conversation
---

# Improvement Request: Make Routine Operations Workstation-Driven

## Request
Routine development and administration for Headscale and Velociportal should not require using the TrueNAS host shell or running `docker exec` on the NAS. Treat the NAS as a production, super-admin system: allow at most a one-time bootstrap there, then prefer workstation-driven API or CLI automation using narrowly scoped, least-privilege credentials.

## Context
The prior repository-guided TrueNAS path assumed shell or SSH access on the NAS for setup, diagnostics, deployment, health checks, and validation. Repeated interactive work on the production storage host increased operational risk and normalized broad administrative access where a safer remote control path was needed.

## Resolution

The canonical TrueNAS journey is now UI-managed and workstation-driven. It permits one short-lived Headscale API-key bootstrap in the app shell, then uses HTTPS-only `headscale-ops`, UI-managed app/network settings, declarative Tailscale Serve, and remote acceptance checks for routine administration. Recurring TrueNAS shell and `docker exec` workflows are no longer canonical.

## Source
Direct user feedback during the Velociportal workflow session on 2026-08-21.

## Related
- [TrueNAS SCALE deployment guide](../guides/truenas-scale.md)
- [Current handoff context](../../knowledgebase/04-handoff-context.md)
