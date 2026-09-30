from webprogress import storage


def test_create_and_resolve_token(tmp_path):
    db = tmp_path / "wp.db"
    storage.init_db(db)
    storage.upsert_user(db, sub="alice", email="alice@example.com", name="Alice")

    token = storage.create_token(db, sub="alice", label="laptop")

    assert storage.resolve_token(db, token) == "alice"


def test_resolve_unknown_token_returns_none(tmp_path):
    db = tmp_path / "wp.db"
    storage.init_db(db)

    assert storage.resolve_token(db, "not-a-real-token") is None
    assert storage.resolve_token(db, "") is None


def test_revoked_token_no_longer_resolves(tmp_path):
    db = tmp_path / "wp.db"
    storage.init_db(db)
    storage.upsert_user(db, sub="bob", email="bob@example.com", name="Bob")
    token = storage.create_token(db, sub="bob", label="ci")

    (row,) = storage.list_tokens(db, "bob")
    storage.revoke_token(db, "bob", row["token_hash"])

    assert storage.resolve_token(db, token) is None
    assert storage.list_tokens(db, "bob") == []


def test_revoke_is_scoped_to_owner(tmp_path):
    db = tmp_path / "wp.db"
    storage.init_db(db)
    storage.upsert_user(db, sub="carol", email="c@example.com", name="Carol")
    token = storage.create_token(db, sub="carol", label="key")
    (row,) = storage.list_tokens(db, "carol")

    # A different user cannot revoke Carol's token.
    storage.revoke_token(db, "mallory", row["token_hash"])

    assert storage.resolve_token(db, token) == "carol"
