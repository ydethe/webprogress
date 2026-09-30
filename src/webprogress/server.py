from dataclasses import dataclass

from authlib.integrations.starlette_client import OAuth
from fastapi import Request, Response
from fastapi.responses import RedirectResponse
from nicegui import Event, app, ui
from starlette.middleware.base import BaseHTTPMiddleware

from . import storage
from .config import settings
from .models import ClientPayload


@dataclass
class RoutedPayload:
    """A payload tagged with the user it belongs to (resolved from the token)."""

    user_sub: str
    payload: ClientPayload


payload_handler = Event[RoutedPayload]()

#: OIDC provider name registered with Authlib.
_PROVIDER = "oidc"
oauth = OAuth()

#: Routes reachable without an authenticated session.
UNRESTRICTED_ROUTES = {"/login", "/auth", "/logout", "/handler"}


@app.post("/handler")
def sensor_webhook(payload: ClientPayload, request: Request):
    """Authenticate the client by its token (carried in ``payload.key``) and route it."""
    user_sub = storage.resolve_token(settings.db_path, payload.key)
    if user_sub is None:
        return Response(status_code=401)
    if request.client is not None:
        payload.user_src_address = request.client.host
    payload_handler.emit(RoutedPayload(user_sub=user_sub, payload=payload))


@app.get("/login")
async def login(request: Request):
    return await oauth.oidc.authorize_redirect(request, settings.redirect_uri)


@app.get("/auth")
async def auth(request: Request):
    token = await oauth.oidc.authorize_access_token(request)
    userinfo = token.get("userinfo") or await oauth.oidc.userinfo(token=token)
    sub = userinfo["sub"]
    email = userinfo.get("email", "")
    name = userinfo.get("name") or userinfo.get("preferred_username") or email or sub
    storage.upsert_user(settings.db_path, sub, email, name)
    app.storage.user.update(
        {"user": {"sub": sub, "email": email, "name": name}, "authenticated": True}
    )
    return RedirectResponse("/")


@app.get("/logout")
def logout():
    app.storage.user.clear()
    return RedirectResponse("/login")


class AuthMiddleware(BaseHTTPMiddleware):
    """Redirect unauthenticated page requests to the login flow."""

    async def dispatch(self, request: Request, call_next):
        if not app.storage.user.get("authenticated", False):
            path = request.url.path
            if not path.startswith("/_nicegui") and path not in UNRESTRICTED_ROUTES:
                return RedirectResponse("/login")
        return await call_next(request)


@ui.page("/")
def root():
    user = app.storage.user.get("user", {})
    sub = user.get("sub")

    with ui.row().classes("w-full items-center justify-between"):
        ui.label(f"webprogress — {user.get('name', '')}").classes("text-xl")
        ui.button("Logout", on_click=lambda: ui.navigate.to("/logout")).props("outline")

    _token_panel(sub)
    _progress_panel(sub)


def _token_panel(sub: str):
    with ui.card().classes("w-full"):
        ui.label("Client tokens").classes("text-lg")
        ui.label("Use a token as WEBPROGRESS_KEY on the client.").classes("text-sm text-grey")
        token_list = ui.column().classes("w-full")

        def make_revoke(token_hash: str):
            def handler():
                storage.revoke_token(settings.db_path, sub, token_hash)
                refresh()

            return handler

        def refresh():
            token_list.clear()
            with token_list:
                for row in storage.list_tokens(settings.db_path, sub):
                    with ui.row().classes("items-center"):
                        ui.label(f"{row['label']} ({row['prefix']}…)")
                        ui.label(row["created_at"]).classes("text-xs text-grey")
                        ui.button("Revoke", on_click=make_revoke(row["token_hash"])).props(
                            "flat color=negative"
                        )

        def generate():
            label = label_input.value or "token"
            plaintext = storage.create_token(settings.db_path, sub, label)
            label_input.value = ""
            refresh()
            with ui.dialog() as dialog, ui.card():
                ui.label("Copy this token now — it will not be shown again:")
                ui.code(plaintext).classes("w-full")
                ui.button("Close", on_click=dialog.close)
            dialog.open()

        with ui.row().classes("items-center"):
            label_input = ui.input("Label").props("dense")
            ui.button("Generate token", on_click=generate)

        refresh()


def _progress_panel(sub: str):
    with ui.card().classes("w-full"):
        ui.label("Live progress").classes("text-lg")
        container = ui.column().classes("w-full")
        #: One (label, bar) per distinct task, keyed by hostname + description.
        bars: dict[str, ui.linear_progress] = {}

        def update(routed: RoutedPayload):
            if routed.user_sub != sub:
                return
            p = routed.payload
            key = f"{p.user_hostname}:{p.description}"
            if key not in bars:
                with container:
                    ui.label(f"{p.description or 'task'} @ {p.user_hostname}")
                    bars[key] = ui.linear_progress(show_value=False)
            bars[key].value = (p.progress / p.total) if p.total else 0.0

        payload_handler.subscribe(update)


def run():
    settings.require_oidc()
    storage.init_db(settings.db_path)

    oauth.register(
        name=_PROVIDER,
        client_id=settings.oidc_client_id,
        client_secret=settings.oidc_client_secret,
        server_metadata_url=settings.oidc_server_metadata_url,
        client_kwargs={"scope": settings.oidc_scope},
    )

    # AuthMiddleware must sit *inside* NiceGUI's Session/RequestTracking middleware
    # so app.storage.user is populated when it runs. Passing storage_secret makes
    # NiceGUI add those (and the SessionMiddleware that also backs Authlib's OAuth
    # state) wrapping this one, since it is added first here.
    app.add_middleware(AuthMiddleware)

    ui.run(port=8775, reload=False, storage_secret=settings.session_secret)
