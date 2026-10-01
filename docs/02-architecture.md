# webprogress — Architecture

This document describes **how** the system's parts fit together: the components, the contract
that couples them, the end-to-end flow of a progress update, and the internal mechanisms for
authentication, routing, sessions, persistence, and configuration. It is written independently of
any particular programming language, library, or framework. For **what** the system must do, see
the companion [`01-specification.md`](./01-specification.md).

---

## 1. Component overview

The system is composed of five logical components. One (the **Progress Reporter**) runs inside
the tracked program; the others run inside the server. A **Shared Update Contract** binds the
reporter to the server.

```
                      tracked program
                 ┌───────────────────────┐
                 │   Progress Reporter    │
                 │  (builds & sends       │
                 │   updates each tick)   │
                 └───────────┬───────────┘
                             │  update message
                             │  (Shared Update Contract)
                             ▼
   server  ┌─────────────────────────────────────────────────┐
           │  Ingest & Routing Service                        │
           │   • authenticate token → user                    │
           │   • stamp source address                         │
           │   • publish on in-process event bus              │
           └───────────────┬──────────────────┬──────────────┘
                           │                  │
             resolve/store │                  │ publish
                           ▼                  ▼
           ┌───────────────────────┐   ┌─────────────────────────┐
           │  Persistence Store    │   │ Web Dashboard & Session │
           │   • users             │◀──│  Layer                  │
           │   • tokens            │   │   • login / session     │
           │                       │   │   • token management    │
           └───────────────────────┘   │   • live indicators     │
                                       └─────────────────────────┘
```

| Component | Responsibility |
| --- | --- |
| **Progress Reporter** | Embedded in the tracked program. Mirrors the local progress display into update messages and sends them best-effort to the server. |
| **Ingest & Routing Service** | Receives update messages, authenticates each by its token, binds it to a user, stamps the sender address, and publishes it for delivery. |
| **Web Dashboard & Session Layer** | Serves the signed-in user's dashboard, manages sign-in sessions, renders live task indicators, and provides the token-management panel. |
| **Persistence Store** | Durable store of users and tokens. |
| **Shared Update Contract** | The single message definition shared by reporter and server. |

---

## 2. The shared contract as the coupling point

The reporter and the server are otherwise independent, but they are **tightly coupled through one
message definition**: the update payload (its fields are listed in the specification, §3). The
reporter serializes exactly these fields; the server validates incoming messages against exactly
these fields. Consequently, **any change to a field changes both sides at once** — adding,
removing, or renaming a field is a contract change, not a local edit. This is the primary
compatibility boundary of the system.

---

## 3. End-to-end data flow

A single progress update travels from a tick in the tracked program to a moving bar in the
browser as follows:

```
 tracked program                server                         browser(s)
 ───────────────                ──────                         ──────────
 1. progress tick
 2. build update
    from local state
 3. send ──────────────▶ 4. receive at ingest
                         5. authenticate token
                            → resolve user
                            (reject if unknown/
                             revoked/empty)
                         6. stamp source address
                         7. publish on
                            in-process event bus ─────┐
                                                      ▼
                                           8. each open dashboard
                                              session is subscribed
                                           9. filter: keep only
                                              updates for this viewer
                                          10. upsert one indicator per
                                              (host, description) and
                                              set its fill = progress/total
```

Steps 1–3 happen in the reporter; steps 4–7 in the Ingest & Routing Service; steps 8–10 in the
Web Dashboard & Session Layer. The send in step 3 uses a short timeout and tolerates failure, so a
stall or rejection at the server never propagates back into the tracked program.

---

## 4. Authentication & routing internals

- **Token resolution at ingest.** The credential token arrives inside the update message. The
  service hashes it and looks it up in the token store, retrieving the owning user. An unknown,
  revoked, or empty token yields no user and the update is rejected as unauthorized.
- **The event bus is the rendering primitive.** Once an update is bound to a user, the service
  publishes it on an **in-process event bus**. Every open dashboard page subscribes to that bus.
  This publish/subscribe step — not direct function calls — is the core mechanism that turns an
  incoming update into a live screen change.
- **Two isolation checkpoints.** User separation is enforced at two independent points: (a) at
  ingest, where a token resolves to exactly one user, and (b) at render, where each subscribed
  dashboard discards any published update whose owning user is not the page's viewer.

---

## 5. Session & middleware model

- A **session-guard middleware** intercepts page requests. If the requester has no authenticated
  session, it is redirected to login.
- A small set of **unrestricted routes** bypass the guard: the login start, the identity-provider
  callback, logout, the update-ingest endpoint, and the health endpoint. Internal framework
  support routes are likewise always allowed.
- The **update-ingest path is intentionally unrestricted** by the session guard: it does not use a
  web session at all and instead self-authenticates by token. Web-UI auth and update auth are two
  separate schemes sharing one server.
- **Ordering requirement.** The session guard must run at a point where the session has already
  been resolved, so that it can read the caller's authentication state. In other words, the guard
  is nested inside whatever establishes the session, not outside it.

The login round-trip itself is a delegation: login redirects the browser to the identity provider;
the provider authenticates the user and calls back to the server's callback route; the callback
exchanges the result for the user's identity, creates or refreshes the user record, and marks the
session authenticated.

---

## 6. Persistence model

The store holds two collections with a one-to-many relationship.

**Users** — one record per person, keyed by the stable subject identifier supplied by the identity
provider:

| Attribute | Meaning |
| --- | --- |
| subject | Primary key; the identity provider's stable user identifier. |
| email | User's email, refreshed on each sign-in. |
| name | Display name, refreshed on each sign-in. |
| created-at | When the record was first created. |

**Tokens** — many per user, keyed by the token's hash:

| Attribute | Meaning |
| --- | --- |
| token hash | Primary key; the irreversible hash of the token value. |
| owning user | The subject of the user who created the token (one user → many tokens). |
| label | Human-chosen name for the token. |
| prefix | A short, non-secret leading fragment of the token value, for display. |
| created-at | When the token was minted. |
| revoked | A flag; revocation is a soft state change, not a deletion. |

The store exposes these **capabilities** (named by what they do, not how):

| Capability | Effect |
| --- | --- |
| Initialise | Ensure the user and token collections exist. |
| Upsert user | Create a user on first sign-in, or refresh email/name on subsequent sign-ins. |
| Create token | Mint a new token for a user, store only its hash and prefix, and return the full value once. |
| Resolve token | Given a token value, return the owning user if the token exists and is not revoked; otherwise nothing. |
| List tokens | Return a user's active (non-revoked) tokens. |
| Revoke token | Mark one of the user's own tokens as revoked, effective immediately. |

Revocation is a soft state change, so a revoked token's hash and metadata remain but can no longer
resolve to a user.

---

## 7. Configuration & runtime

The server requires the following configuration to operate. Values are supplied generically
through the server's environment (or an equivalent configuration source):

| Setting | Purpose |
| --- | --- |
| Identity-provider coordinates | The client identifier, secret, and discovery location used to delegate sign-in. |
| Requested scopes | The set of identity claims requested from the provider (defaults to basic identity, email, and profile). |
| Public base address | The externally reachable base address, used to construct the identity-provider **callback** location. |
| Session signing secret | Secret used to sign session state; must be set to a strong value in production. |
| Store location | Where the persistence store lives. |

Runtime characteristics:

- The service listens on a **fixed port, 8775**.
- The callback location is **derived** from the public base address by appending the callback path.
- **Startup validation**: before serving, the server verifies that the required identity-provider
  settings are present and refuses to start otherwise. The remaining settings have safe defaults.
- When configuration is logged at startup, **sensitive values are masked**.
- The server is intended to run as a single self-contained service instance (for example, inside a
  container with a persistent volume for the store and a health probe against the health endpoint).

---

## 8. Security model (structural view)

The specification (§7) states the security requirements; here they are mapped onto the structure.

- **Trust boundaries.** Three zones meet at the server: (a) the **public ingest surface**, reachable
  by any reporter and guarded only by token authentication; (b) the **authenticated UI surface**,
  guarded by the session mechanism; and (c) the **external identity provider**, trusted to assert
  user identity. The ingest surface and the UI surface authenticate by entirely separate schemes.
- **Where credentials live.** The reporter's credential token lives in the update **message body**,
  not in a transport header. In the store, only the token's **hash and prefix** are kept; the full
  value exists only transiently at creation time.
- **Hashing at rest.** Because tokens are stored hashed, a disclosure of the store does not reveal
  usable tokens.
- **The double isolation checkpoint.** User separation does not rely on any single check: it is
  enforced both at ingest (token → one user) and at render (per-viewer filtering), so a mistake in
  one place does not by itself leak another user's tasks.
- **Live-delivery boundary.** Live updates flow through an **in-process event bus**. That bus is the
  boundary of live delivery: it exists only within a running service instance and does not itself
  carry updates across instances or survive a restart. This is the natural seam to revisit if live
  delivery must span more than one service instance.

---

## 9. Extension points

Two seams account for most changes:

- **The shared update contract.** Anything affecting what data a task reports — new display fields,
  changed semantics — is a contract change touching both the reporter (which fills the field) and
  the dashboard (which renders it). These move together.
- **The event bus.** Anything affecting how updates are delivered live — additional rendering,
  alternative views, multi-instance delivery — attaches at the publish/subscribe step.

As a concrete example: adding a newly rendered field means (1) extending the shared contract so the
reporter emits it and the service accepts it, and (2) extending the dashboard to display it. No
other component needs to change.
