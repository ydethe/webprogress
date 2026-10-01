# WebProgress

`webprogress` lets you track a long-running task from a web UI. On the client
side it is a drop-in [`tqdm`](https://tqdm.github.io/) replacement: it renders
the usual terminal progress bar *and* streams each update to a running
webprogress server, which shows it live in your browser.

```python
from webprogress_python import Tracker
from webprogress_python.config import settings

# A Tracker groups every bar it opens under one script name on the dashboard.
with Tracker(script="basic.py", endpoint=settings) as t:
    for a in t.tqdm(range(10), desc="foo"):
        ...
    for a in t.tqdm(range(10), desc="bar"):
        ...
```

`key` and `endpoint` may also be provided via the `WEBPROGRESS_KEY` and
`WEBPROGRESS_HOST` environment variables. If the server is down, slow, or
rejects the request, the tracked loop is never blocked or broken — the update is
simply dropped.

Each update carries a **`script`** field so the dashboard can group related
tasks. The dashboard nests them three levels deep — **script ▸ deployable
(the host/user running it) ▸ task** — so one script running on several machines
shows one group per machine, each with its own bars. Tasks reported without a
script fall into a shared "unscripted" group.

### Version handshake

The server advertises its identity and the wire **protocol version** from an
unauthenticated endpoint, so a client can discover it *before* reporting and
adapt the protocol it speaks:

```bash
curl http://localhost:8775/version
# {"name":"webprogress","version":"dev","protocol":3}
```

The handshake is advisory — a client that skips it still works against a
compatible server. `version` is the server build (overridable at build time, see
below); `protocol` is bumped whenever the shared wire contract changes in a way
clients must adapt to (for example, the addition of the `script` field, or the
per-run `uuid` a client sends so its restarts open fresh dashboard cards).

## How it works

- **Client** (`webprogress.tqdm`, Python) — subclasses `tqdm.auto.tqdm` and
  overrides `display()`. Each tick builds a `ClientPayload` and POSTs it to
  `{endpoint}/handler` with a short timeout.
- **Server** (Go) — an OIDC-authenticated web app. Users log in via their OIDC
  provider, mint client tokens from the dashboard, and watch a live progress bar
  per task (pushed over a WebSocket). The `/handler` endpoint authenticates each
  payload by its token and routes it to the owning user's dashboard. The server
  is wire-compatible with the Python client — the JSON contract is unchanged.

## Running the server

The server authenticates users with an OIDC provider (e.g. Authentik) and stores
users and tokens in SQLite. Configure it through `WEBPROGRESS_*` environment
variables (or a `.env` file):

| Variable | Purpose |
| --- | --- |
| `WEBPROGRESS_OIDC_CLIENT_ID` | OIDC client id (required) |
| `WEBPROGRESS_OIDC_CLIENT_SECRET` | OIDC client secret (required) |
| `WEBPROGRESS_OIDC_SERVER_METADATA_URL` | Provider discovery URL (required) |
| `WEBPROGRESS_OIDC_SCOPE` | Requested scopes (default `openid email profile`) |
| `WEBPROGRESS_BASE_URL` | Public base URL, used to build the `/auth` redirect (default `http://127.0.0.1:8775`) |
| `WEBPROGRESS_SESSION_SECRET` | Secret signing session cookies — set a strong value in production |
| `WEBPROGRESS_DB_PATH` | SQLite database path (default `webprogress.db`) |
| `WEBPROGRESS_DEFAULT_UPDATE_INTERVAL_SECONDS` | Expected seconds between a task's updates, assumed only until the server observes the task's own reporting cadence; from it a silent task is aged to stalled (after 2×) then dead (after 10×) (default `30`; `0` disables the fallback) |
| `WEBPROGRESS_NOTIFY_CHANNEL` | Default notification channel: `pushover`, `slack`, `webhook`, or empty for off |
| `WEBPROGRESS_NOTIFY_PUSHOVER_TOKEN` / `_USER` | Pushover application token and user/group key |
| `WEBPROGRESS_NOTIFY_SLACK_WEBHOOK_URL` | Slack incoming-webhook URL |
| `WEBPROGRESS_NOTIFY_WEBHOOK_URL` | Custom webhook URL (receives a JSON event payload) |
| `WEBPROGRESS_NOTIFY_WEBHOOK_HEADER` | Optional extra header for the custom webhook, as `Name: Value` (e.g. `Authorization: Bearer …`) |
| `WEBPROGRESS_NOTIFY_STALL_SECONDS` | Alert when a task goes this many seconds without an update (`0` = off) |

### Notifications

The server can alert you out-of-band when a tracked task **completes** or
**stalls** (goes silent for longer than the stall timeout). Three channels are
supported: [Pushover](https://pushover.net), a Slack *incoming webhook*, or a
custom webhook that receives a JSON body (`{event, title, message, task}`) and
can carry an optional custom header (e.g. an `Authorization` bearer token).

Configuration is **per user** and lives behind the **Settings** menu in the
dashboard navigation bar — pick a channel, fill in the credentials, set a stall
timeout, and use **Test** to send a sample. The `WEBPROGRESS_NOTIFY_*` env vars
above provide a server-wide default used only until a user saves their own
settings; **a user's saved settings always take precedence over the env default**,
and **Reset to server defaults** removes them again.

Then build and start it:

```bash
go build -o webprogress ./cmd/webprogress
./webprogress
```

The version advertised at `/version` defaults to `dev`; stamp a real build
version with the linker:

```bash
go build -ldflags "-X github.com/ydethe/webprogress/internal/server.Version=$(git describe --tags)" \
  -o webprogress ./cmd/webprogress
```

The app serves on port **8775**. Log in, generate a token, and use it as your
client's `key` / `WEBPROGRESS_KEY`.

### Testing without authentication

Pass `--noauth` to disable authentication entirely — for local testing only:

```bash
./webprogress --noauth
```

No OIDC configuration is required, the dashboard opens without a login, and
`POST /handler` accepts updates with no token (every update is routed to a single
local user). **Never run this in production.**

### Docker

A `Dockerfile` builds an image that runs only the server. Supply the
`WEBPROGRESS_*` variables at runtime:

```bash
docker build -t webprogress .
docker run -p 8775:8775 --env-file .env webprogress
```

## Development

The server is a Go module (`github.com/ydethe/webprogress`). The published Docker
image is tagged from git tags, so releases require tagging.

```bash
go build ./...     # compile all packages
go test ./...      # run the test suite
go vet ./...       # static checks
gofmt -w .         # format
```
