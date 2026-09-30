from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Server configuration, read from ``WEBPROGRESS_*`` environment variables.

    The OIDC fields are required to run the server but are left optional here so
    the module can be imported (and doctests collected) without a provider
    configured; :func:`webprogress.server.run` validates them at startup.
    """

    model_config = SettingsConfigDict(
        env_prefix="WEBPROGRESS_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    #: OIDC client id registered with the provider (e.g. Authentik).
    oidc_client_id: str = ""
    #: OIDC client secret.
    oidc_client_secret: str = ""
    #: Provider discovery document, e.g. ``https://auth.example.com/application/o/app/.well-known/openid-configuration``.
    oidc_server_metadata_url: str = ""
    #: Scopes requested during login.
    oidc_scope: str = "openid email profile"
    #: Public base URL of this server, used to build the ``/auth`` redirect URI.
    base_url: str = "http://127.0.0.1:8775"
    #: Secret used to sign session cookies. Set a strong random value in production.
    session_secret: str = "change-me"
    #: Path to the SQLite database holding users and tokens.
    db_path: Path = Field(default=Path("webprogress.db"))

    #: Field names whose values must never be printed to the terminal.
    _SENSITIVE = frozenset({"oidc_client_secret", "session_secret"})

    @property
    def redirect_uri(self) -> str:
        return f"{self.base_url.rstrip('/')}/auth"

    def log_config(self) -> None:
        """Print the resolved server configuration, masking sensitive secrets."""
        print("webprogress settings:")
        for name in self.__class__.model_fields:
            value = "***" if name in self._SENSITIVE else getattr(self, name)
            print(f"  {name} = {value}")
        print(f"  redirect_uri = {self.redirect_uri}")

    def require_oidc(self) -> None:
        """Raise a clear error if the OIDC provider is not configured."""
        missing = [
            name
            for name in ("oidc_client_id", "oidc_client_secret", "oidc_server_metadata_url")
            if not getattr(self, name)
        ]
        if missing:
            raise RuntimeError(
                "Missing OIDC configuration: "
                + ", ".join(f"WEBPROGRESS_{m.upper()}" for m in missing)
            )


settings = Settings()
settings.log_config()
