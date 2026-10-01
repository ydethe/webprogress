# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`webprogress` lets a developer track a long-running task from a web UI. The **client** is a Python
drop-in `tqdm` replacement (`from webprogress import tqdm`, maintained on the `master` branch) that,
in addition to rendering a normal terminal progress bar, POSTs each display update to a running
**server**. This branch (`go`) is a **Go reimplementation of the server** only; it stays wire-compatible
with the existing Python client (the JSON contract is unchanged). The server authenticates each update,
routes it to the sending user, and renders it live in that user's browser dashboard.

The framework-independent specs in `docs/01-specification.md` (what) and `docs/02-architecture.md` (how)
are the authoritative description of behaviour; the Go code implements them.

## Architecture

Go module `github.com/ydethe/webprogress`. The entrypoint is `cmd/webprogress/main.go`; the server lives
under `internal/`, split into packages connected by the shared payload model:

- **`internal/models`** — `ClientPayload` is the wire contract shared with the Python client. The JSON
  field names (`user_hostname`, `script`, `progress`, `total`, `colour`, `key`, …) must match the client
  exactly; changing one is a contract change. Numeric fields are `float64`. `key` authenticates/routes the
  sender and is not displayed. `script` groups tasks on the dashboard and is part of a task's identity.
  `TaskKey` (script+host+description) is the logical task key the dispatcher groups by. `uuid` is the
  **client-assigned per-run identity** (one per tqdm run): `InstanceKey` returns it, falling back to
  `TaskKey` for a pre-v3 client, and that key is the dashboard card's identity so a restart (new uuid)
  opens a fresh card instead of reviving the old one.
  `ScriptName` (script with an "(unscripted)" fallback), `Fraction`,
  `RemainingTime`, and `ETA` are derived helpers, never serialized. Liveness carries no wire field:
  the server judges it from the task's update cadence (see `cadenceTracker` below), deriving the
  `stall`/`dead` silence thresholds; `Status(idle, stall, dead)` yields the task's `TaskStatus`
  (`running`, `finished`, `stalled`, `dead`) the dashboard shows and filters on, with `StallMultiple`/
  `DeadMultiple` (2 and 10) the cadence multiples. `ProtocolVersion` (currently 3; v3 added the
  client-assigned `uuid`) is the wire-protocol
  version and `ServerInfo` is the handshake response the server advertises from `GET /version` so clients
  can adapt the protocol before reporting; bump `ProtocolVersion` on any contract change clients must adapt
  to.
- **`internal/config`** — `Settings` loaded from `WEBPROGRESS_*` env vars (and an optional `.env` via
  godotenv). `RequireOIDC()` fails fast if the OIDC settings are missing; `RedirectURI()` derives the
  `/auth` callback from `BaseURL`; `LogConfig()` logs settings with secrets masked. The `WEBPROGRESS_NOTIFY_*`
  vars feed `DefaultNotify()`, the server-wide fallback notification config (a persisted per-user config
  always overrides it). `DefaultUpdateIntervalSeconds` (`WEBPROGRESS_DEFAULT_UPDATE_INTERVAL_SECONDS`,
  default 30) is the fallback update cadence the `cadenceTracker` assumes until it has observed a
  task's own (i.e. for the first update), so even a report-once-and-die task is aged out; `0` disables
  the fallback.
- **`internal/notify`** — out-of-band task alerts, independent of the Python wire contract. `Config`
  describes one user's channel (`pushover`, `slack`, `webhook`, or empty/off) plus a `StallSeconds`
  timeout; `Send` delivers a `Message` over it. The `Dispatcher` subscribes to the `bus.Hub` and fires a
  one-shot notification when a task completes (fraction ≥ 1) or stalls (no update within `StallSeconds`);
  all task bookkeeping runs on its single `Run` goroutine, so only the outbound HTTP send is off-loaded.
  A `Resolver` func supplies each user's effective `Config`. This package imports neither `config` nor
  `storage`, so both depend on it without a cycle.
- **`internal/storage`** — SQLite (`modernc.org/sqlite`, pure Go, no cgo) with `users` and `tokens`
  tables. Users are keyed by OIDC `sub`. Tokens are minted server-side; only their SHA-256 hash and a
  short prefix are stored, so the plaintext is shown once at creation. A token is bound to the user that
  created it — that binding is how an incoming payload is routed. The `notify_settings` table holds each
  user's persisted notification config. Methods: `Open`/`initDB`, `UpsertUser`, `CreateToken`,
  `ResolveToken`, `ListTokens`, `RevokeToken`, `GetNotifyConfig`, `SaveNotifyConfig`, `ClearNotifyConfig`.
- **`internal/bus`** — the in-process pub/sub `Hub`. The ingest handler `Publish`es a `RoutedPayload`
  (payload + `UserSub` + `InstanceID` + `StallSeconds`/`DeadSeconds`); each open dashboard connection
  `Subscribe`s. This publish/subscribe step is the core rendering mechanism. `Publish` is non-blocking
  (drops to a full subscriber) so a slow dashboard never stalls ingest. `InstanceID` is the per-run
  identity — the client's `uuid` (via `payload.InstanceKey()`) — used as the dashboard's card key, so a
  restart of the same task (new uuid) opens a new card instead of reviving the old one;
  `StallSeconds`/`DeadSeconds` are the silence thresholds the server's `cadenceTracker` derives from the
  task's observed update cadence.
- **`internal/auth`** — OIDC login (`coreos/go-oidc` + `golang.org/x/oauth2`) and the session cookie
  (`gorilla/sessions`, signed with `SESSION_SECRET`). `Login`/`Callback`/`Logout` run the OIDC round-trip
  and establish the session; `Middleware` redirects unauthenticated page requests to `/login`, while the
  `unrestricted` routes (`/login`, `/auth`, `/logout`, `/handler`, `/health`, `/version`) and `/static/`
  bypass it.
- **`internal/server`** — HTTP routing and handlers. `POST /handler` resolves `payload.Key` to a user via
  `storage.ResolveToken` (**401** if unknown/revoked/empty), stamps `UserSrcAddress` from the request
  host, and derives the run's `InstanceID` from the client's `uuid` (`payload.InstanceKey()`, falling
  back to `TaskKey` for a pre-v3 client). It calls `cadenceTracker.observe` (see `cadence.go`), keyed by
  that run id, for the `stall_seconds`/`dead_seconds` silence thresholds derived from the task's observed
  update cadence (EWMA of the gaps between its updates, floored against jitter, falling back to the
  configured default until the cadence is known); it then publishes a `RoutedPayload`. `GET /version`
  advertises `models.ServerInfo` (name, the
  build-time `Version` var, and `models.ProtocolVersion`) for the client handshake; it is unauthenticated
  like `/health`. `GET /` renders the dashboard; `GET /ws` upgrades to a WebSocket (`gorilla/websocket`),
  subscribes to the hub, and forwards only updates where `UserSub` matches the viewer (per-user isolation
  at render). The WebSocket frame carries `script` plus the cadence-derived silence thresholds
  (`stall_seconds`, `dead_seconds`), and the dashboard groups tasks three levels deep —
  **script ▸ deployable (host/login) ▸ task** — in `web/static/app.js`. Running and finished tasks share one
  "Tasks" section; each card shows a status badge and a one-second sweep ages silent tasks from running to
  stalled to dead against those thresholds, with a status filter (running only by default) deciding what shows. `POST /tokens/create` and
  `/tokens/revoke` manage tokens; the new token's plaintext is shown once via a session flash.
  `POST /settings/notify`, `/settings/notify/reset`, and `/settings/notify/test` manage per-user
  notification settings from the dashboard's Settings menu; `effectiveNotify` resolves a user's config
  (persisted settings if present, else the env default). HTML template and JS live under
  `internal/server/web/` and are embedded with `go:embed`. `Run(noAuth)` loads config, initialises the DB,
  builds auth (validating OIDC and the provider, unless `noAuth`), starts the `notify.Dispatcher` on the
  hub, and serves on port **8775**. With `noAuth` (the `--noauth` CLI flag, testing only) OIDC is skipped:
  `auth.NewNoAuth` treats every request as the fixed `auth.NoAuthSub` user and bypasses the page guard, and
  `handleIngest` routes updates to that user without a token.

Authentication (token → user) happens in `POST /handler` via `storage.ResolveToken`; per-user routing
happens again in the WebSocket loop via the `UserSub` filter — the two isolation checkpoints described in
the architecture doc. Trace `RoutedPayload` from `handleIngest` through `bus.Hub` to `handleWS` before
changing progress rendering.

## Commands

Standard Go toolchain (Go 1.26). The server is a pure-Go build (`CGO_ENABLED=0`) thanks to the modernc
SQLite driver.

```bash
go build ./...                       # compile all packages
go build -o webprogress ./cmd/webprogress   # build the server binary
go test ./...                        # run the full test suite
go test ./internal/storage/          # run a single package's tests
go vet ./...                         # static checks
gofmt -w .                           # format
```

The server (`Run()`) needs OIDC configuration via `WEBPROGRESS_*` env vars (or a `.env` file); see the
README and `sample.env` for the full list. `./webprogress --healthcheck` probes the local `/health`
endpoint and exits 0/1 (used by the container healthcheck). A `Dockerfile` builds a distroless
server-only image.

Notes on the layout:

- `cmd/webprogress/main.go` is the only entrypoint; everything else is under `internal/`.
- Web assets (`internal/server/web/templates/dashboard.html`, `internal/server/web/static/app.js`) are
  embedded via `go:embed`, so the binary is self-contained. The dashboard uses Tailwind via CDN.
- The wire contract (`internal/models`) is the compatibility boundary with the Python client on `master`:
  a field change must be mirrored there too.
