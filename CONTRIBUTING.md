# Contributing to Velociportal

Fork the repository, make your change, run one command, and open a pull request. The command, `make check`, runs the same Make targets that GitHub-hosted CI runs on every pull request. It needs no credentials, `.env` file, registry login, NAS, tailnet, or running upstream services.

Read [`AGENTS.md`](AGENTS.md) before changing behavior. It holds the locked architecture, security, and verification rules for every contributor and coding agent. The [README](README.md) and [known limitations](docs/reference/known-limitations.md) describe the public scope.

## Prerequisites

| Tool | Version | Used by |
|---|---|---|
| GNU Make | any recent | Runs every stage |
| Go | 1.22 (CI pins 1.22.12; `go.mod` declares `go 1.22`) | `gofmt`, `go vet`, `go test`, binary build |
| C compiler (for example `gcc`) with cgo enabled | any | `go test -race` requires cgo on Linux |
| Docker Engine | 28.0+ | Image build and image smoke checks |
| Docker Compose plugin (`docker compose`) | 2.33.1+ | Contributor and production Compose rendering |
| python3 with pip | 3.9+ (CI uses 3.12) | Compose verification (standard library) and documentation tooling |
| MkDocs Material | pinned in `docs/requirements.txt` | Strict documentation build |
| POSIX shell, `git` | — | Make recipes; `git` only stamps the build revision and is optional |

Install the documentation tools once in an activated Python virtual environment (keep that environment active when running `make check`):

```sh
python3 -m pip install -r docs/requirements.txt
```

Network access is needed to install these Python dependencies and the first time Go downloads modules (`golang.org/x/term`, `golang.org/x/sys`). `docker build` also fetches the digest-pinned `golang:1.22-alpine` builder image and Alpine CA certificates. A non-root or rootless Docker daemon works; nothing is published to a registry or bound to a LAN port.

Not needed for `make check`: Node.js, Headscale, Tailscale, Nginx Proxy Manager, TrueNAS, or any OAuth client.

## The one command

```sh
make check
```

`check` is declared `.NOTPARALLEL`, so its stages run one at a time even under `make -j`. Each stage maps to a CI step:

| Stage | Make target | CI job → step |
|---|---|---|
| Formatting | `fmt-check` (`gofmt -l *.go`) | `test` → gofmt |
| Static analysis | `vet` (`go vet ./...`) | `test` → go vet |
| Unit and fixture tests | `test` (`go test -v -race -count=1 ./...`) | `test` → go test |
| Static binary and CLI smoke | `cli-smoke` (`CGO_ENABLED=0` build, then `healthcheck`/`validate`/`suggest-hostnames --help`) | `build` → Build binary and CLI smoke check |
| Compose rendering | `compose-check` (contributor Compose, private-CA overlay, `scripts/verify-production-compose.py`) | `docker` → Validate Compose configuration |
| Image contract | `image-check` (local `docker build`, read-only `--help` runs, healthcheck and `65534:65534` user assertions) | `docker` → Build image and smoke check |
| Documentation | `docs-check` (`mkdocs build --strict`) | `docs` → Build documentation strictly |

CI passes build-stamp variables (`BUILD_VERSION=ci`, `GIT_REVISION`, `GIT_SOURCE_STATE=clean`) and tags the image `velociportal:test`; locally these default to `dev`, your `HEAD`, the working-tree state, and `velociportal:latest`. The checks themselves are identical.

For a faster docs-only iteration, `make docs-check` runs just the documentation stage. The complete `make check` always includes it, matching the separate CI `docs` job.

Optional, not run by CI: the dependency-free browser-controller checks `node scripts/test-service-editor.mjs` and `node scripts/test-personal-services.mjs`. `make verify` is a broader maintainer target that also renders every optional Compose overlay combination.

## What a green `make check` proves

It proves that the code is formatted, vets cleanly, passes the Go unit and fixture tests with the race detector, builds a static binary and a non-root image whose CLI starts, that the Compose bundles render with the expected security shape from the tracked example files, and that the documentation builds with strict warnings enabled.

It does **not** prove live behavior. Tests use synthetic fixtures for Headscale, Tailscale SaaS, and NPM responses. Real integration validation still requires the canonical TrueNAS path with at least two identities, LAN-negative tests, NPM control-proxy checks, and card sets compared against actual control-plane reachability. Tailscale SaaS additionally needs the live OAuth, role-mapping, policy-negative, and SSH acceptance listed in `AGENTS.md`. Do not describe a passing PR as live, deployment, or support validation.

## Publication is separate from PR verification

Neither publication workflow is required for contributing or included in `make check`.

- **`deploy-docs`** runs only on pushes to `main`, not on pull requests. It needs `contents: write` to publish GitHub Pages. Forks inherit this push-to-main condition; it is not restricted to the upstream repository.
- **`Release`** (`.github/workflows/release.yml`) is a manual `workflow_dispatch` from an immutable tag, run by maintainers. Its optional `publish` job pushes to GHCR with `packages: write`. Contributors never need it, and published release images are never replaced or retagged.

## Pull requests

- Keep each pull request to one purpose and keep `make check` green.
- Do not commit `.env` files, real policies, credentials, private hostnames, or certificates. `.gitignore` covers the common names; only `*.example` templates belong in the repository.
- Do not add AI tools or systems as commit co-authors.
- Private, machine-specific agent notes belong in an untracked `CLAUDE.local.md`, which is gitignored.
