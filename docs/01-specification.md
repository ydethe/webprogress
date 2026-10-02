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
| `script` | Name of the script (or program) the task belongs to. Tasks sharing a script are grouped together on the dashboard, and the script is part of a task's identity (see §6.3). May be empty, in which case tasks fall into a shared "unscripted" group. |
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
| `uuid` | Identity of this particular **run** of the task, assigned by the reporter (one value per run — see §6.3). The server draws one dashboard card per uuid, so a restarted task, sending a new uuid, opens a new card instead of reviving the previous run's. Added in protocol version 3; a pre-v3 reporter omits the field and the server falls back to the (`script`, `user_hostname`, `description`) triple, which cannot tell successive runs apart. |
| `tags` | Optional list of free-form labels attached to the task (e.g. `["gpu", "nightly"]`). Shown as chips on the dashboard and available as a filter dimension; they are **not** part of a task's identity (see §6.3). Added in protocol version 2; a pre-v2 reporter omits the field. |
| `library` | Name of the client library that is reporting this task (e.g. `"webprogress"`). Combined with `library_version` it is shown on the dashboard as the chip `library@library_version` (e.g. `webprogress@1.2.3`) and is available as a filter dimension (see §6.3); it is **not** part of a task's identity. Added in protocol version 4; an earlier reporter omits the field. |
| `library_version` | Version of the reporting client library (e.g. `"1.2.3"`), shown together with `library`. Added in protocol version 4; an earlier reporter omits the field. |
| `criticity` | Importance of the task, chosen by the reporter, that decides which out-of-band notifications fire for it (see §6.7). One of `trivial`, `standard`, or `critical`; an absent or unrecognised value is treated as `standard` (the default). Shown on the dashboard as a colour-coded chip and available as a filter dimension (see §6.3); it is **not** part of a task's identity. Added in protocol version 4; an earlier reporter omits the field, falling back to `standard`. |

The reporter advertises **nothing about liveness**: whether a task has stalled or
died is judged entirely by the server from how often the task reports (see §6.3),
so there is no heartbeat field to send.

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

### 3.5 Full example

A complete update message, as sent by the reporter in the body of a single ingest
request. All fields are present; `user_src_address` is empty because the server
stamps it on receipt, and `key` carries the credential token.

```json
{
  "user_hostname": "gpu-node-03",
  "user_login": "ydethe",
  "user_src_address": "",
  "script": "train.py",
  "progress": 1280,
  "total": 5000,
  "description": "epoch 3/12",
  "elapsed": 42.5,
  "unit": "batch",
  "unit_scale": true,
  "rate": 30.1,
  "unit_divisor": 1000,
  "initial": 0,
  "colour": "#3b82f6",
  "key": "wp_8f3a1c9e4b7d2056",
  "uuid": "f0e1d2c3-b4a5-6789-0123-456789abcdef",
  "tags": ["gpu", "nightly"],
  "library": "webprogress",
  "library_version": "1.2.3",
  "criticity": "critical"
}
```

From this message the server derives `remaining_time = (5000 − 1280) / 30.1 ≈
123.6` seconds and an `eta` of the receipt time plus that interval; neither is
carried on the wire (see §3.4). An older reporter omits the fields its protocol
predates — a pre-v2 reporter sends no `tags`, and a pre-v4 reporter no `library`,
`library_version`, or `criticity` (which then defaults to `standard`).

---

## 4. Client-reporter behaviour

The reporter is a drop-in replacement for an ordinary progress-bar facility. It behaves
identically for the local display and adds the reporting side effect.

Optionally, **before it begins reporting**, the reporter may perform the version handshake (§6.6):
it reads the server's advertised protocol version and adapts the update message accordingly. This
step is advisory and best-effort — skipping it, or failing to reach the server, never prevents the
reporter from running.

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
- A **task** is identified by the triple *(script, origin host, description)*. Two updates sharing
  that triple update the same indicator; a new triple creates a new indicator with a label
  identifying the task.
- A task's indicator tracks a single **run**, identified by the reporter-assigned `uuid` (§3.1).
  Two updates sharing a uuid drive the same indicator. When a task **restarts**, the reporter mints a
  new uuid for the new run, so the server gives it a **fresh indicator** rather than reviving the
  previous one (which may have aged to *stalled* or *dead*). The run identity is kept out of the way:
  the triple above is unchanged, so the new run is grouped and labelled exactly like the old one. A
  pre-v3 reporter that sends no uuid falls back to the triple as the run key, which cannot distinguish
  successive runs — restarting such a task reuses the old indicator.
- Tasks are shown in a **three-level grouping**: by **script** at the top, then by **deployable**
  (the origin host and the login running it), then the individual **tasks** as subitems. A single
  script running on several hosts therefore shows one group per host underneath it, each with its
  own tasks. Tasks whose `script` is empty are grouped together under a shared "unscripted" group.
- Each indicator's fill is the fraction `progress / total` (shown as empty when `total` is zero).
- Every task lives in a **single "Tasks" section** — running and finished tasks are shown together,
  distinguished by their status badge rather than by separate sections.
- Each task carries a **status**, one of:
  - **running** — in progress and reporting normally;
  - **finished** — reached 100% (`progress / total` ≥ 1), stays finished regardless of later
    silence;
  - **stalled** — has gone silent for more than **twice** its usual interval between updates but may
    still recover;
  - **dead** — has gone silent for more than **ten times** its usual interval between updates, so the
    task is presumed gone; it is dropped from the default view.

  Status is **derived**, never carried on the wire: it follows from the task's fraction and how long
  it has been silent relative to its **update cadence** — the interval at which it normally reports,
  which the server measures (an exponentially-weighted average of the gaps between received updates,
  and so proportional to the task's rate). The reporter advertises nothing about liveness. Until the
  server has seen a task report twice it does not yet know the cadence, and falls back to a
  configurable default interval (`WEBPROGRESS_DEFAULT_UPDATE_INTERVAL_SECONDS`, 30s by default) so
  that even a report-once-and-die task is eventually aged out; setting that default to `0` leaves such
  a task *running* until its cadence is known. A very fast cadence is floored so ordinary network
  jitter does not flap a task between *running* and *stalled*.
- A task's `tags` are shown as **chips** on its indicator. Clicking a chip adds that tag to the
  tag filter. The reporting `library` is shown as its own `library@library_version` chip, and the
  task's `criticity` as a **colour-coded chip** (one colour per level: trivial, standard, critical).
- The dashboard offers a **filter** over four text dimensions — **host**, **script**, **task**
  (description), and **library** — plus a **tags** filter, a **criticity** filter, and a **status**
  filter, applied together (a task must match every set dimension). Host, script, task, and library
  match as case-insensitive substrings (the library matches against the `library@library_version`
  label, so either the name or the version narrows it); the tag filter requires every listed tag to
  be present on the task; the criticity filter keeps only tasks whose criticity level is selected
  (**all three shown by default**); the status filter keeps only tasks whose status is selected
  (**only *running* shown by default**; the other statuses are revealed by selecting them). Groups
  with no matching task are hidden while a filter is active.
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

### 6.6 Version advertisement (handshake)

- The server exposes an **unauthenticated version endpoint** that advertises its identity, build
  version, and the **protocol version** of the wire contract it speaks.
- A client may perform this **handshake first**, before it starts reporting, to discover the
  protocol version and **adapt the update message it sends** to what the server supports — for
  example, only populating fields that the advertised protocol understands.
- The handshake is advisory: because reporting is best-effort (§4), a client that skips the
  handshake still works against a compatible server, and a client that queries it never blocks the
  tracked task on the result.
- The **protocol version** is incremented whenever the shared contract (§3) changes in a way that
  clients must adapt to. The current protocol version is **4**, which added the reporting `library`
  and `library_version` and the task `criticity`; version 3 added the reporter-assigned per-run
  `uuid`; version 2 added the optional `tags` field, and version 1 covered the addition of the
  `script` field. (Liveness is judged by the server from the update cadence, so it needs no wire field
  and no version bump — see §6.3.)

### 6.7 Out-of-band notifications

Independently of the live dashboard, the server may deliver a **one-shot notification** to a user's
configured channel when one of their tasks reaches a notable moment. Three moments are recognised:

- **complete** — the task reached 100%;
- **dead** — the task went silent past its **dead** threshold (ten update cadences, see §6.3) and is
  presumed gone;
- **stall** — the task went silent past a shorter, user-configured timeout but may still recover.

Which of these actually fire for a task is gated by its **`criticity`** (§3.1):

| Criticity | Fires on |
| --- | --- |
| `trivial` | nothing — the task is never announced. |
| `standard` (default) | **dead** only — the user is told only when the task is presumed gone. |
| `critical` | **stall**, **dead**, and **complete** — every moment is announced. |

Each moment fires **at most once** per occurrence; a fresh update from a task that had gone silent
re-arms its stall/dead notifications. Delivery is best-effort and never blocks ingestion or the
dashboard. The channel itself (e.g. push, chat webhook, or a custom HTTP endpoint) and the stall
timeout are part of a user's configuration, out of scope of this contract.

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
| `/version` | GET | None | Advertise the server's name, build version, and wire **protocol version** for the client handshake (§6.6). |
| `/login` | GET | None | Begin sign-in by delegating to the identity provider. |
| `/auth` | GET | None | Identity-provider callback; establishes the session and returns to the dashboard. |
| `/logout` | GET | None | Clear the local session and return to login. |
| `/` | GET (page) | Session | The dashboard: live task indicators plus the token-management panel. |

All page paths other than those marked "None" require an authenticated session; unauthenticated
requests to them are redirected to login.
