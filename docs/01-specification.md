# webprogress — Functional Specification

This document specifies **what** the system does: its purpose, actors, the data contract
exchanged between its parts, and the behaviour required of each part. It is written to be
independent of any particular programming language, library, or framework — it describes
components, functionalities, and contracts only. The companion document,
[`02-architecture.md`](./02-architecture.md), describes **how** the parts fit together.

---

## 1. Purpose & overview

`webprogress` lets a developer watch a long-running task from a web browser.

A **progress reporter** embedded in the tracked program emits one update every time the task's
progress display advances. In addition to drawing the usual local (terminal) progress bar, the
reporter mirrors each update to a central **server**. The server authenticates the update,
determines which user it belongs to, and renders it live as a progress indicator in that user's
private web dashboard.

The design goal is **transparency to the tracked task**: reporting is a best-effort side effect.
If the server is down, slow, or rejecting, updates are silently dropped and the tracked task
continues unaffected.

---

## 2. Actors & roles

| Actor | Role |
| --- | --- |
| **Tracked program** | Runs the long task; hosts the progress reporter that emits updates. |
| **Authenticated web user** | A person who signs in to the web UI, mints and manages credential tokens, and watches their own live dashboards. |
| **Server** | Receives updates, authenticates them, routes them to the owning user, and renders them; also serves the web UI and manages tokens. |
| **Identity provider** | An external authority that authenticates web users on the server's behalf. The server delegates all user sign-in to it. |

A single web user may have many tracked programs reporting at once; each reporting program
carries a credential token that ties its updates back to that one user.

---

## 3. Progress update contract (the wire payload)

Every progress update is a single structured message with a fixed set of fields. This message is
the contract between reporter and server: both sides must agree on it. Fields fall into three
categories — **display** data (shown to the user), **routing/metadata**, and the **credential**.

### 3.1 Display fields

| Field | Meaning |
| --- | --- |
| `user_hostname` | Host name of the machine running the tracked program. Also used to identify a task (see §6.3). |
| `user_login` | Account name of the user running the tracked program on that machine. |
| `progress` | Number of completed units of work so far. |
| `total` | Expected total number of units of work. |
| `description` | Short label for the task. Also used to identify a task (see §6.3). |
| `elapsed` | Seconds elapsed since the task started. |
| `unit` | Name of one unit of work (for display, e.g. "items"). |
| `unit_scale` | Whether units should be automatically scaled with magnitude prefixes. |
| `rate` | Current processing rate, in units per second. |
| `unit_divisor` | Divisor used when scaling units. |
| `initial` | Starting count for a task that resumed from a non-zero point. |
| `colour` | Preferred colour for the progress indicator. |

### 3.2 Routing / metadata field

| Field | Meaning |
| --- | --- |
| `user_src_address` | Source network address of the sender. The reporter leaves this empty; the server fills it in from the received request. It is authoritative only when set by the server. |

### 3.3 Credential field

| Field | Meaning |
| --- | --- |
| `key` | The credential token that authenticates the sender and selects which user the update is routed to. It is **not** a display field. |

### 3.4 Derived values (not transmitted)

Two values are **computed** from the fields above and are never carried on the wire:

- `remaining_time` — estimated seconds to completion, computed as `(total − progress) / rate`.
- `eta` — absolute estimated completion time, computed as the current time plus `remaining_time`.

Because `remaining_time` divides by `rate`, it is only meaningful once a non-zero rate exists.

---

## 4. Client-reporter behaviour

The reporter is a drop-in replacement for an ordinary progress-bar facility. It behaves
identically for the local display and adds the reporting side effect.

On **each display tick**, the reporter must:

1. Render the local progress bar exactly as a normal progress bar would — this happens first and
   always, independently of any reporting.
2. Capture the current progress state and build one update message from it.
3. Apply fallback defaults for values that are not yet available:
   - `rate` defaults to `0`,
   - `initial` defaults to `0`,
   - `colour` defaults to a standard blue.
4. Attach the credential token (`key`) and leave `user_src_address` empty.
5. Send the update to the server's update-ingest endpoint using a **short send timeout**.

The reporter must be **non-blocking and fault-tolerant**:

- Any transport-level failure (server unreachable, timeout, connection refused, …) is caught and
  ignored.
- A rejection response from the server is likewise ignored; the reporter does not inspect the
  response.
- Under no circumstances may reporting slow down beyond the short timeout, raise an error into
  the tracked task, or alter the local progress bar.

The reporter sends nothing on a progress-bar clear; clearing affects only the local display.

---

## 5. Configuration inputs (reporter)

The reporter needs two inputs:

| Input | Meaning |
| --- | --- |
| **Credential token** | The token minted in the web UI that authenticates and routes this program's updates. |
| **Server base address** | The base location of the server to which updates are sent. |

Each input may be supplied **directly by the caller** when constructing the reporter, or through
the program's **environment**. A value supplied directly takes precedence over the environment.
Neither input is validated by the reporter: if a value is missing the reporter still runs, and
any resulting update is simply rejected or undeliverable.

---

## 6. Server-side functional requirements

### 6.1 Update ingestion

- The server exposes an **update-ingest endpoint** that accepts a progress update message.
- The credential token travels **inside the update message**, not as a separate header.
- The server resolves the token to its owning user. If the token is unknown, revoked, or empty,
  the server responds with an **unauthorized** result and does nothing further.
- On success, the server stamps the update's `user_src_address` with the sender's source address
  and routes the update to the resolved user's live dashboards.

### 6.2 Web session

- Any request for a protected page by a user who is not signed in is **redirected to login**.
- Login **delegates to the external identity provider**; the server does not store passwords.
- When the provider returns the user, the server creates or refreshes that user's record and
  establishes an authenticated session.
- Logout clears the local session. (It ends the local session only; it does not perform a
  provider-side sign-out.)

### 6.3 Dashboard

- The dashboard displays **one live progress indicator per task**.
- A **task** is identified by the pair *(origin host, description)*. Two updates sharing that pair
  update the same indicator; a new pair creates a new indicator with a label identifying the task
  and its origin host.
- Each indicator's fill is the fraction `progress / total` (shown as empty when `total` is zero).
- A user sees **only their own tasks**: updates routed to other users never appear.

### 6.4 Token management

Within the web UI, a signed-in user can:

- **Generate** a token under a chosen label. The full token value is shown **exactly once**, at
  creation, and cannot be retrieved again afterward.
- **List** their active tokens, each shown by its label, a short non-secret prefix, and its
  creation time.
- **Revoke** a token. Revocation takes effect immediately, and a user may only revoke tokens that
  belong to them.

### 6.5 Liveness

- The server exposes an **unauthenticated health endpoint** that reports a basic "ok" status, for
  use by external monitors.

---

## 7. Security model

- **Update authentication is token-based.** Each update carries a credential token in its body;
  the server authenticates and routes the update solely by that token.
- **Tokens are stored only as irreversible hashes**, together with a short non-secret prefix used
  for display. The full token value is revealed once at creation and is never persisted, so it
  cannot be recovered from the store.
- **Each token is bound to exactly one user.** This binding is the routing mechanism: resolving a
  token yields the user whose dashboards the update is delivered to.
- **Per-user isolation is enforced twice**: once when an incoming update is bound to a user at
  ingestion, and again when each dashboard discards any update not belonging to its viewer.
- **Web-UI authentication is session-based** and delegated to the external identity provider. A
  separate, unrestricted path handles update ingestion and self-authenticates by token,
  independent of any web session.
- **Sensitive configuration values** (such as the identity-provider secret and the session signing
  secret) are masked wherever configuration is logged.

---

## 8. External interface summary

| Path | Method | Auth required | Outcome |
| --- | --- | --- | --- |
| `/handler` | POST | Token (in body) | Ingest one update; **unauthorized** if the token is unknown/revoked/empty, otherwise routed to the owning user. |
| `/health` | GET | None | Report basic liveness status. |
| `/login` | GET | None | Begin sign-in by delegating to the identity provider. |
| `/auth` | GET | None | Identity-provider callback; establishes the session and returns to the dashboard. |
| `/logout` | GET | None | Clear the local session and return to login. |
| `/` | GET (page) | Session | The dashboard: live task indicators plus the token-management panel. |

All page paths other than those marked "None" require an authenticated session; unauthenticated
requests to them are redirected to login.
