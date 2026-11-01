# AGENTS.md

peerglass is a small, informative web UI for rTorrent. Read
[docs/requirements.md](docs/requirements.md) before changing anything. It
defines what the UI does and, just as importantly, what it must never do.

## Sources of truth

- **[docs/requirements.md](docs/requirements.md):** what to build. Its
  requirement IDs (F, S, N) are referenced in commits and tests.
- **[docs/rtorrent.md](docs/rtorrent.md):** rTorrent's API as used here, and
  its quirks.
- **[docs/testing.md](docs/testing.md):** what to test and how.
- **`../ansible-infrastructure`:** how peerglass is deployed. The Compose
  stack, the rTorrent image and `rtorrent.rc` all live in
  `docker-compose/app01.infra.aslot-int.dk/rtorrent/`. This repository holds
  the application and its Dockerfile only.

## Ground rules

- **Keep it simple.** Build what the requirements ask for, well. Ask before
  adding features from "Later" or beyond.
- **No packages (S7).** Go standard library only; no npm, no TypeScript, no
  build step. htmx is the only third-party code in the browser, vendored and
  pinned, with its SHA-256 next to the file and checked by a test. Docker base
  images are pinned by digest. Ask the user before adding any dependency.
- **The server renders HTML (S2).** Use `html/template` for everything. Never
  build HTML from strings or mark external data as safe. JavaScript of our own
  lives in `internal/web/static/*.js`, stays small, and only focuses,
  positions, toggles and asks htmx to fetch; it never builds markup from data
  or evaluates strings (a test checks). Prefer an htmx attribute when one does
  the job.
- **The browser never speaks rTorrent (S1).** Each action is a handler that
  sends one fixed command with validated arguments. Don't add a generic RPC
  endpoint, and never call `execute.*`, `method.*` or `schedule*`.
- **No passkeys anywhere (S5).** Reduce tracker URLs to scheme and host inside
  the rTorrent client code, before they reach templates or logs.
- **No persistent settings (F12).** rTorrent's configuration is `rtorrent.rc`
  in `../ansible-infrastructure`. The UI may only make changes that end with
  an rTorrent restart: the temporary global limits (F13) are the one such
  setting. Ask before adding another. Per-browser presentation (F14) lives
  in a cookie; peerglass stores nothing.
- **Don't duplicate rtorrent.rc.** Path guards, label routing and port sync
  already happen in rTorrent (requirements §1). peerglass validates its input,
  but doesn't move data or decide destinations itself.

## Layout

| Path | Contents |
| --- | --- |
| `cmd/peerglass/` | `main`: configuration from the environment, wiring, the HTTP server |
| `internal/rtorrent/` | SCGI and XML-RPC client, and typed calls for exactly the commands in docs/rtorrent.md |
| `internal/web/` | Handlers, `templates/`, and `static/` (CSS and vendored htmx), embedded with `embed` |
| `docs/` | Requirements, rTorrent notes and testing |

Create packages when the code needs them. These are the intended homes, not
empty folders to add up front.

## Commands

Run all commands from the repository root.

| Command | What it does |
| --- | --- |
| `make check` | gofmt check, `go vet`, `go test ./...`; must pass before every commit |
| `make test` | `go test ./...` |
| `make build` | Builds a static `bin/peerglass` (`CGO_ENABLED=0`) |
| `go test -tags integration ./...` | Optional tests against a real rTorrent container (docs/testing.md) |

Releases are version tags on `main` (`vX.Y.Z`): the release workflow tests,
builds and publishes `ghcr.io/kabsdk/peerglass:X.Y.Z` and prints its digest.
Tag only when the user asks; deploying means pinning that version and digest
in `../ansible-infrastructure`.

## Working with the lab

- **Production is off limits.** The rTorrent on app01 seeds about 825
  private torrents. Never point peerglass, tests or experiments at it.
  Develop against a local rTorrent container with throwaway torrents.
- **Read-only checks on app01 only when the task asks.** Access is root over
  SSH with `~/.ssh/ansible_proxmox` (see `../ansible-infrastructure/AGENTS.md`).
- **Deployment changes are made in `../ansible-infrastructure`,** following
  its AGENTS.md, and only when the task asks for them. That covers the Compose
  service, image version pins and `rtorrent.rc` methods (for example, the one
  F10 needs).
- **Commit or push only when the user asks.**

## Finishing a task

- `make check` passes.
- Docs are updated when behaviour, configuration or the rTorrent commands in
  use change.
- Report what you tested and how, including anything you checked by hand,
  and anything you could not test.
