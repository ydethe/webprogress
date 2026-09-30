# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`webprogress` is an early-stage (MVP) Python library that lets a developer track a long-running task from a web UI. The public API is a drop-in `tqdm` replacement (`from webprogress import tqdm`) that, in addition to rendering a normal terminal progress bar, POSTs each display update to a running web server which renders it live.

## Architecture

Three modules under `src/webprogress/`, connected by a shared payload model:

- **`client.py`** — `tqdm` subclasses `tqdm.auto.tqdm` and overrides `display()`. On every display tick it builds a `ClientPayload` from `self.format_dict` and fires `requests.post(f"{endpoint}/handler", ...)` with a short timeout (`ConnectTimeout` is swallowed, so the tracked loop never blocks or fails if the server is down). `key`/`endpoint` come from constructor kwargs or the `WEBPROGRESS_KEY` / `WEBPROGRESS_ENDPOINT` env vars.
- **`server.py`** — a [NiceGUI](https://nicegui.io) app. A FastAPI route `@app.post("/handler")` validates the incoming JSON as a `ClientPayload`, stamps `user_src_address` from the request, and emits it on a NiceGUI `Event[ClientPayload]`. The `root` page subscribes to that event to update a `ui.linear_progress` bar. `run()` serves on port **8775**.
- **`models.py`** — `ClientPayload` (Pydantic `BaseModel`) is the wire contract shared by both sides; changing a field affects client serialization and server validation together. Derived `remaining_time` and `eta` are computed properties, not stored fields.

The event-driven `Event`/`emit`/`subscribe` flow between the webhook route and the UI page is the core mechanism — trace it through `server.py` before changing progress rendering.

### Known MVP limitations (relevant when extending)

- The server wires a single global progress bar; there is **no per-client / per-key routing** yet — every payload updates the same bar regardless of `key`, `user_login`, or `user_hostname`.
- The `key` field is sent but **not authenticated** server-side.
- `client.py` imports `requests`, which is present only transitively (not in `[project].dependencies`); add it explicitly to `pyproject.toml` if you rely on it.

## Commands

Package/deps are managed with **uv** (`uv.lock`), built with **pdm-backend**. Version is derived from git tags via SCM, so releases require tagging.

```bash
uv sync                    # install all dependency groups into .venv
uv run pytest              # run the full test suite (config in pyproject.toml)
uv run pytest tests/test_client.py::test_client   # run a single test
uv run black .             # format (line length 100)
```

Notes on the test config (`[tool.pytest.ini_options]`):

- `asyncio_mode = "auto"` and `--doctest-modules` are on: docstring examples in `src/` are executed as tests, and async tests need no explicit marker.
- pytest writes an HTML report + JUnit XML under `htmldoc/` and collects coverage against the `webprogress` package (config in `tests/coverage.conf`).
- `test_client` expects a server at `http://127.0.0.1:8775` and `test_server` calls `run()` (blocking) — these are manual/integration checks, not isolated unit tests. Run the server in one terminal, the client test in another.
