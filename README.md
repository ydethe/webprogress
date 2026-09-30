# WebProgress

`webprogress` lets you track a long-running task from a web UI. On the client
side it is a drop-in [`tqdm`](https://tqdm.github.io/) replacement: it renders
the usual terminal progress bar *and* streams each update to a running
webprogress server, which shows it live in your browser.

```python
from webprogress import tqdm

for a in tqdm(range(10), key="wbk_xxxxxxxxxxx", endpoint="http://localhost:8775"):
    ...
```

`key` and `endpoint` may also be provided via the `WEBPROGRESS_KEY` and
`WEBPROGRESS_ENDPOINT` environment variables. If the server is down, slow, or
rejects the request, the tracked loop is never blocked or broken — the update is
simply dropped.

## How it works

- **Client** (`webprogress.tqdm`) — subclasses `tqdm.auto.tqdm` and overrides
  `display()`. Each tick builds a `ClientPayload` and POSTs it to
  `{endpoint}/handler` with a short timeout.
- **Server** (`webprogress.server`) — a [NiceGUI](https://nicegui.io) app. Users
  log in via OIDC, mint client tokens from the dashboard, and watch a live
  progress bar per task. The `/handler` webhook authenticates each payload by its
  token and routes it to the owning user's dashboard.

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

Then start it:

```bash
uv run python -c "from webprogress.server import run; run()"
```

The app serves on port **8775**. Log in, generate a token, and use it as your
client's `key` / `WEBPROGRESS_KEY`.

### Docker

A `Dockerfile` builds an image that runs only the server. Supply the
`WEBPROGRESS_*` variables at runtime:

```bash
docker build -t webprogress .
docker run -p 8775:8775 --env-file .env webprogress
```

## Development

Dependencies are managed with [uv](https://docs.astral.sh/uv/) and built with
`pdm-backend`. The version is derived from git tags, so releases require tagging.

```bash
uv sync            # install all dependency groups into .venv
uv run pytest      # run the test suite
uv run black .     # format (line length 100)
```
