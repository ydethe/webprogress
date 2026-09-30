"""SQLite-backed storage for users and client tokens.

Users are identified by their OIDC ``sub``. Tokens are opaque secrets minted on
the server after a successful login; only their SHA-256 hash is stored, so the
plaintext is shown to the user exactly once at creation time. A token is bound
to the user that created it, which is how an incoming progress payload is routed
to the right dashboard.
"""

import hashlib
import secrets
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

#: Number of leading characters of a token shown to the user for identification.
PREFIX_LEN = 8


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _hash(plaintext: str) -> str:
    return hashlib.sha256(plaintext.encode("utf-8")).hexdigest()


def _connect(path: str | Path) -> sqlite3.Connection:
    conn = sqlite3.connect(str(path))
    conn.row_factory = sqlite3.Row
    return conn


def init_db(path: str | Path) -> None:
    """Create the ``users`` and ``tokens`` tables if they do not exist."""
    with _connect(path) as conn:
        conn.executescript("""
            CREATE TABLE IF NOT EXISTS users (
                sub        TEXT PRIMARY KEY,
                email      TEXT,
                name       TEXT,
                created_at TEXT
            );
            CREATE TABLE IF NOT EXISTS tokens (
                token_hash TEXT PRIMARY KEY,
                user_sub   TEXT NOT NULL,
                label      TEXT,
                prefix     TEXT,
                created_at TEXT,
                revoked    INTEGER NOT NULL DEFAULT 0
            );
            """)


def upsert_user(path: str | Path, sub: str, email: str, name: str) -> None:
    """Insert the user or refresh their email/name on subsequent logins."""
    with _connect(path) as conn:
        conn.execute(
            """
            INSERT INTO users (sub, email, name, created_at)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(sub) DO UPDATE SET email = excluded.email, name = excluded.name
            """,
            (sub, email, name, _now()),
        )


def create_token(path: str | Path, sub: str, label: str) -> str:
    """Mint a token bound to ``sub`` and return the plaintext (shown once)."""
    plaintext = secrets.token_urlsafe(32)
    with _connect(path) as conn:
        conn.execute(
            """
            INSERT INTO tokens (token_hash, user_sub, label, prefix, created_at, revoked)
            VALUES (?, ?, ?, ?, ?, 0)
            """,
            (_hash(plaintext), sub, label, plaintext[:PREFIX_LEN], _now()),
        )
    return plaintext


def resolve_token(path: str | Path, plaintext: str) -> str | None:
    """Return the ``user_sub`` bound to a valid, non-revoked token, else ``None``."""
    if not plaintext:
        return None
    with _connect(path) as conn:
        row = conn.execute(
            "SELECT user_sub FROM tokens WHERE token_hash = ? AND revoked = 0",
            (_hash(plaintext),),
        ).fetchone()
    return row["user_sub"] if row else None


def list_tokens(path: str | Path, sub: str) -> list[sqlite3.Row]:
    """List a user's non-revoked tokens (label, prefix, created_at, token_hash)."""
    with _connect(path) as conn:
        return conn.execute(
            """
            SELECT token_hash, label, prefix, created_at
            FROM tokens WHERE user_sub = ? AND revoked = 0
            ORDER BY created_at DESC
            """,
            (sub,),
        ).fetchall()


def revoke_token(path: str | Path, sub: str, token_hash: str) -> None:
    """Revoke one of the user's tokens (scoped to ``sub`` so users can't revoke others')."""
    with _connect(path) as conn:
        conn.execute(
            "UPDATE tokens SET revoked = 1 WHERE token_hash = ? AND user_sub = ?",
            (token_hash, sub),
        )
