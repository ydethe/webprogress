# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`webprogress` is an early-stage Python library that lets a developer track a long-running task from a web UI. The public API is a drop-in `tqdm` replacement (`from webprogress import tqdm`) that, in addition to rendering a normal terminal progress bar, POSTs each display update to a running web server which authenticates it, routes it to the sending user, and renders it live.

## Architecture

Five modules under `src/webprogress/`, connected by a shared payload model:

- **`client.py`** — `tqdm` subclasses `tqdm.auto.tqdm` and overrides `display()`. On every display tick it builds a `ClientPayload` from `self.format_dict` and fires `requests.post(f"{endpoint}/handler", ...)` with a short timeout. Any `requests.exceptions.RequestException` is swallowed, so the tracked loop never blocks or fails if the server is down, slow, or rejecting. `key`/`endpoint` come from constructor kwargs or the `WEBPROGRESS_KEY` / `WEBPROGRESS_ENDPOINT` env vars. The `key` is the client token minted in the web UI.
- **`server.py`** — a [NiceGUI](https://nicegui.io) app with OIDC login. The FastAPI route `@app.post("/handler")` resolves `payload.key` to a user via `storage.resolve_token` (returns **401** if unknown/revoked), stamps `user_src_address`, and emits a `RoutedPayload` (payload + `user_sub`) on a NiceGUI `Event[RoutedPayload]`. The `root` page subscribes and updates **one `ui.linear_progress` per task** (keyed by hostname + description), filtered to the logged-in user. It also renders a token-management panel (generate/list/revoke). `AuthMiddleware` redirects unauthenticated page requests to `/login`; `UNRESTRICTED_ROUTES` (`/login`, `/auth`, `/logout`, `/handler`) bypass it. `run()` validates OIDC config, initialises the DB, registers the provider, and serves on port **8775**.
- **`config.py`** — `Settings` (Pydantic `BaseSettings`, `WEBPROGRESS_` env prefix, `.env` support) holds OIDC/session/DB configuration. Instantiated at import as the module-level `settings`, which also calls `log_config()` (masking `_SENSITIVE` fields). OIDC fields default to empty so the module imports without a provider; `run()` enforces them via `require_oidc()`.
- **`storage.py`** — SQLite-backed `users` and `tokens` tables. Users are keyed by OIDC `sub`. Tokens are minted server-side; only their SHA-256 hash is stored, so the plaintext is shown once at creation. A token is bound to the user that created it — this binding is how an incoming payload is routed. Functions: `init_db`, `upsert_user`, `create_token`, `resolve_token`, `list_tokens`, `revoke_token`.
- **`models.py`** — `ClientPayload` (Pydantic `BaseModel`) is the wire contract shared by both sides; changing a field affects client serialization and server validation together. `key` authenticates/routes the sender and is not a display field. Derived `remaining_time` and `eta` are computed properties, not stored fields.

The event-driven `Event`/`emit`/`subscribe` flow between the webhook route and the UI page is the core rendering mechanism — trace `RoutedPayload` through `server.py` before changing progress rendering. Authentication (token → user) happens in the `/handler` route via `storage.resolve_token`; per-user routing happens in the page's `subscribe` callback via the `user_sub` filter.

## Commands

Package/deps are managed with **uv** (`uv.lock`), built with **pdm-backend**. Version is derived from git tags via SCM, so releases require tagging. `requests` is an explicit runtime dependency.

```bash
uv sync                    # install all dependency groups into .venv
uv run pytest              # run the full test suite (config in pyproject.toml)
uv run pytest tests/test_storage.py   # run a single test module
uv run black .             # format (line length 100)
```

The server (`run()`) needs OIDC configuration supplied via `WEBPROGRESS_*` env vars (or a `.env` file); see the README for the full list. A `Dockerfile` builds a server-only image.

Notes on the test config (`[tool.pytest.ini_options]`):

- `asyncio_mode = "auto"` and `--doctest-modules` are on: docstring examples in `src/` are executed as tests, and async tests need no explicit marker. Note that importing a module runs its top-level code — `config.py` builds `settings` and calls `log_config()` at import.
- pytest writes an HTML report + JUnit XML under `htmldoc/` and collects coverage against the `webprogress` package (config in `tests/coverage.conf`).
- `test_client`/`test_server` are manual/integration checks expecting a live server at `http://127.0.0.1:8775`, not isolated unit tests. Run the server in one terminal and the client test in another. `test_storage` is a self-contained unit test.
