// Dashboard live client. Opens a WebSocket to the server and upserts one row per
// task key (script:host:description) into a flat table with Started, Host, Script,
// Task, Tags and Status columns; rows are sorted by start time, most recent first,
// and the Tags cell shows each tag as a clickable chip. The Status cell carries a
// compact progress bar that stays visible while the row is folded; clicking a row
// unfolds a details panel underneath it with a full-width bar plus ETA, rate, ….
// Each task carries a
// status badge — running, finished, stalled, or dead — derived from its progress
// and how long it has gone silent relative to how often it normally reports; a
// status filter (running only by default) decides which rows are shown. The server
// only sends updates belonging to the logged-in viewer.
(function () {
  "use strict";

  const dot = document.getElementById("conn-dot");
  const text = document.getElementById("conn-text");
  const empty = document.getElementById("empty");
  const taskCount = document.getElementById("task-count");
  const tbody = document.getElementById("tasks");

  const cards = new Map(); // task key -> row (carrying its current values & status)

  function setStatus(state) {
    const styles = {
      connecting: ["bg-amber-400", "connecting…"],
      online: ["bg-emerald-400", "live"],
      offline: ["bg-rose-500", "disconnected"],
    };
    const [cls, label] = styles[state];
    dot.className = "h-2 w-2 rounded-full " + cls;
    text.textContent = label;
  }

  function cssColour(colour) {
    // The client may send a CSS name ("green") or a hex value ("#00ff00").
    return colour || "#6366f1";
  }

  // ---- formatting helpers ----------------------------------------------------

  function fmtDuration(seconds) {
    if (!isFinite(seconds) || seconds <= 0) return "—";
    seconds = Math.round(seconds);
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = seconds % 60;
    if (h) return h + "h " + m + "m " + s + "s";
    if (m) return m + "m " + s + "s";
    return s + "s";
  }

  function fmtDate(d) {
    if (!d || isNaN(d.getTime())) return "—";
    return d.toLocaleString();
  }

  function fmtNum(n) {
    if (typeof n !== "number" || !isFinite(n)) return "—";
    return Number.isInteger(n) ? String(n) : n.toFixed(2);
  }

  // ---- status ----------------------------------------------------------------

  // The four lifecycle states, mirroring models.TaskStatus on the server. Each
  // maps to the badge classes and label shown on a row.
  const STATUS_STYLES = {
    running: ["bg-emerald-500/15 text-emerald-300", "running"],
    finished: ["bg-sky-500/15 text-sky-300", "finished"],
    stalled: ["bg-amber-500/15 text-amber-300", "stalled"],
    dead: ["bg-rose-500/15 text-rose-300", "dead"],
  };
  const BADGE_BASE =
    "status shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums ";

  // Derive a row's current status from its latest frame and how long it has been
  // silent. A finished task (value ≥ 1) stays finished; a task that has gone quiet
  // for longer than the server's stall/dead thresholds (derived from how often it
  // normally reports) goes stalled then dead. While those thresholds are zero
  // (cadence not yet known) the task can only be running or finished.
  function cardStatus(card) {
    if (card.value >= 1) return "finished";
    const idle = (Date.now() - card.lastSeen) / 1000;
    if (card.deadSeconds > 0 && idle >= card.deadSeconds) return "dead";
    if (card.stallSeconds > 0 && idle >= card.stallSeconds) return "stalled";
    return "running";
  }

  // Recompute and render a row's status badge; returns true when it changed.
  function refreshStatus(card) {
    const s = cardStatus(card);
    if (s === card.status) return false;
    card.status = s;
    const [cls, label] = STATUS_STYLES[s];
    card.statusEl.className = BADGE_BASE + cls;
    card.statusEl.textContent = label;
    return true;
  }

  // ---- table rows ------------------------------------------------------------

  // The detail rows revealed when a row is unfolded, in display order. Each key
  // matches a data-f attribute set up in buildCard and updated in fill.
  const FIELDS = [
    ["eta", "ETA"],
    ["elapsed", "Elapsed"],
    ["remaining", "Remaining"],
    ["progress", "Progress"],
    ["rate", "Rate"],
    ["login", "User"],
    ["src", "Source"],
  ];

  const CHEVRON =
    '<svg class="chev h-4 w-4 text-slate-500 transition-transform" viewBox="0 0 20 20" fill="currentColor"><path d="M7 5l6 5-6 5V5z"/></svg>';

  function buildCard() {
    // The summary row and its (initially hidden) details row travel together.
    const row = document.createElement("tr");
    row.className = "cursor-pointer select-none align-middle transition hover:bg-slate-900/40";
    row.innerHTML =
      '<td class="py-2.5 pl-4 pr-2">' +
      '  <span class="flex items-center gap-2">' + CHEVRON +
      '    <span class="started whitespace-nowrap text-xs tabular-nums text-slate-400">—</span>' +
      "  </span>" +
      "</td>" +
      '<td class="host py-2.5 px-2 text-slate-300"></td>' +
      '<td class="script py-2.5 px-2 text-slate-400"></td>' +
      '<td class="name py-2.5 px-2 font-medium text-slate-100"></td>' +
      '<td class="py-2.5 px-2"><div class="tags flex flex-wrap gap-1"></div></td>' +
      '<td class="py-2.5 pl-2 pr-4">' +
      '  <span class="flex items-center gap-2">' +
      '    <span class="status shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums"></span>' +
      '    <span class="minitrack h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-slate-800">' +
      '      <span class="minibar block h-full rounded-full transition-[width] duration-300 ease-out" style="width:0%"></span>' +
      "    </span>" +
      '    <span class="pct text-xs tabular-nums text-slate-400"></span>' +
      "  </span>" +
      "</td>";

    const detail = document.createElement("tr");
    detail.className = "detail hidden bg-slate-900/30";
    const td = document.createElement("td");
    td.colSpan = 6;
    td.className = "px-4 pb-4 pt-1";
    td.innerHTML =
      '<div class="h-2.5 w-full overflow-hidden rounded-full bg-slate-800">' +
      '  <div class="bar h-full rounded-full transition-[width] duration-300 ease-out" style="width:0%"></div>' +
      "</div>" +
      '<dl class="mt-3 grid gap-x-8 gap-y-1 text-xs sm:grid-cols-2"></dl>';
    detail.appendChild(td);

    const dl = td.querySelector("dl");
    for (const [key, label] of FIELDS) {
      dl.innerHTML +=
        '<div class="flex justify-between gap-3 border-b border-slate-800/60 py-1">' +
        '  <dt class="text-slate-500">' + label + "</dt>" +
        '  <dd class="truncate text-right text-slate-300" data-f="' + key + '">—</dd>' +
        "</div>";
    }

    const fields = {};
    td.querySelectorAll("[data-f]").forEach((n) => (fields[n.dataset.f] = n));

    const card = {
      row: row,
      detail: detail,
      chev: row.querySelector(".chev"),
      startedEl: row.querySelector(".started"),
      hostEl: row.querySelector(".host"),
      scriptEl: row.querySelector(".script"),
      name: row.querySelector(".name"),
      bar: td.querySelector(".bar"),
      minibar: row.querySelector(".minibar"),
      pct: row.querySelector(".pct"),
      statusEl: row.querySelector(".status"),
      tags: row.querySelector(".tags"),
      fields: fields,
      status: null,
      open: false,
      value: 0,
      startedAt: 0,
      lastSeen: 0,
      stallSeconds: 0,
      deadSeconds: 0,
      meta: { host: "", login: "", script: "", description: "", tags: [] },
    };

    row.addEventListener("click", () => toggle(card));
    return card;
  }

  function toggle(card) {
    card.open = !card.open;
    card.detail.classList.toggle("hidden", !card.open);
    card.chev.classList.toggle("rotate-90", card.open);
  }

  // Render a task's tags as chips in its Tags column. Clicking a chip adds it to
  // the Tags filter.
  function renderTags(card, tags) {
    const list = Array.isArray(tags) ? tags.filter(Boolean) : [];
    card.tags.innerHTML = "";
    for (const tag of list) {
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className =
        "chip inline-flex items-center rounded-full bg-slate-800 px-2 py-0.5 text-[11px] text-slate-300 transition hover:bg-indigo-500/20 hover:text-indigo-200";
      chip.textContent = tag;
      chip.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation(); // don't fold the row
        addTagToFilter(tag);
      });
      card.tags.appendChild(chip);
    }
  }

  function fill(card, msg) {
    card.name.textContent = msg.label || msg.description || "—";
    card.hostEl.textContent = msg.host || "—";
    card.scriptEl.textContent = msg.script || "(unscripted)";

    const pct = Math.max(0, Math.min(1, msg.value)) * 100;
    const colour = cssColour(msg.colour);
    card.bar.style.width = pct.toFixed(1) + "%";
    card.bar.style.backgroundColor = colour;
    card.minibar.style.width = pct.toFixed(1) + "%";
    card.minibar.style.backgroundColor = colour;
    card.pct.textContent = pct.toFixed(0) + "%";

    // Liveness bookkeeping: remember this frame's arrival and the silence
    // thresholds so the sweep can later age the task into stalled/dead.
    card.value = msg.value;
    card.lastSeen = Date.now();
    card.stallSeconds = typeof msg.stall_seconds === "number" ? msg.stall_seconds : 0;
    card.deadSeconds = typeof msg.dead_seconds === "number" ? msg.dead_seconds : 0;

    // Capture the start time once (from the first frame's elapsed) so the row's
    // position in the most-recent-first sort stays stable across updates.
    if (!card.startedAt && typeof msg.elapsed === "number") {
      card.startedAt = Date.now() - msg.elapsed * 1000;
      card.startedEl.textContent = fmtDate(new Date(card.startedAt));
    }

    const isDone = msg.value >= 1;
    const eta = isDone ? null : (msg.eta ? new Date(msg.eta) : null);
    const unit = msg.unit || "it";

    const f = card.fields;
    f.eta.textContent = isDone ? "done" : fmtDate(eta);
    f.elapsed.textContent = fmtDuration(msg.elapsed);
    f.remaining.textContent = isDone ? "—" : fmtDuration(msg.remaining);
    f.progress.textContent = fmtNum(msg.progress) + " / " + fmtNum(msg.total) + " " + unit;
    f.rate.textContent =
      typeof msg.rate === "number" && msg.rate > 0 ? fmtNum(msg.rate) + " " + unit + "/s" : "—";
    f.login.textContent = msg.login || "—";
    f.src.textContent = msg.src_address || "—";

    renderTags(card, msg.tags);
    card.meta = {
      host: msg.host || "",
      login: msg.login || "",
      script: msg.script || "(unscripted)",
      description: msg.description || "",
      tags: Array.isArray(msg.tags) ? msg.tags.filter(Boolean) : [],
    };
    rememberTags(card.meta.tags);
  }

  // Re-append every row/detail pair ordered by start time, most recent first.
  // appendChild moves existing nodes, so this re-sorts the table in place.
  function reorder() {
    const sorted = Array.from(cards.values()).sort(
      (a, b) => b.startedAt - a.startedAt
    );
    for (const c of sorted) {
      tbody.appendChild(c.row);
      tbody.appendChild(c.detail);
    }
  }

  function upsert(msg) {
    let card = cards.get(msg.key);
    const isNew = !card;
    if (isNew) {
      card = buildCard();
      cards.set(msg.key, card);
    }

    fill(card, msg);
    if (isNew) reorder(); // place the new row by its (just-captured) start time
    refreshStatus(card);
    applyFilter(card);
    recount();
  }

  // A row and its detail share one visibility decision; the detail only shows
  // when the row is both visible and unfolded.
  function setVisible(card, visible) {
    card.row.classList.toggle("hidden", !visible);
    card.detail.classList.toggle("hidden", !(visible && card.open));
  }

  function recount() {
    let total = 0;
    for (const c of cards.values()) if (!c.row.classList.contains("hidden")) total++;
    empty.classList.toggle("hidden", total > 0);
    taskCount.textContent = String(total);
  }

  // Sweep every second so a task that stops reporting ages into stalled then
  // dead on its own, without needing a fresh frame from the server.
  function sweep() {
    let changed = false;
    for (const c of cards.values()) if (refreshStatus(c)) changed = true;
    if (changed) applyFilter(); // re-evaluate visibility + recount
  }

  // ---- filtering -------------------------------------------------------------

  const filterEls = {
    host: document.getElementById("f-host"),
    script: document.getElementById("f-script"),
    task: document.getElementById("f-task"),
    tags: document.getElementById("f-tags"),
  };
  const statusBoxes = Array.from(
    document.querySelectorAll('#f-status input[type="checkbox"]')
  );
  const filterActive = document.getElementById("filter-active");
  const tagCloud = document.getElementById("tag-cloud");
  const seenTags = new Set(); // every tag ever seen, for the quick-filter cloud

  function parseTagFilter(raw) {
    return raw
      .split(/[,\s]+/)
      .map((t) => t.trim().toLowerCase())
      .filter(Boolean);
  }

  function selectedStatuses() {
    const set = new Set();
    for (const box of statusBoxes) if (box.checked) set.add(box.value);
    return set;
  }

  function currentFilter() {
    return {
      host: filterEls.host.value.trim().toLowerCase(),
      script: filterEls.script.value.trim().toLowerCase(),
      task: filterEls.task.value.trim().toLowerCase(),
      tags: parseTagFilter(filterEls.tags.value),
      statuses: selectedStatuses(),
    };
  }

  // The status filter is "active" whenever it is not the default (running only).
  function statusFilterActive(statuses) {
    return !(statuses.size === 1 && statuses.has("running"));
  }

  // A row matches when every set text filter is a (case-insensitive) substring of
  // the corresponding field, every requested tag is present, and its current
  // status is among the selected ones.
  function cardMatches(card, f) {
    const meta = card.meta;
    if (f.host && !meta.host.toLowerCase().includes(f.host)) return false;
    if (f.script && !meta.script.toLowerCase().includes(f.script)) return false;
    if (f.task && !meta.description.toLowerCase().includes(f.task)) return false;
    if (f.tags.length) {
      const have = meta.tags.map((t) => t.toLowerCase());
      for (const want of f.tags)
        if (!have.some((t) => t.includes(want))) return false;
    }
    if (!f.statuses.has(card.status)) return false;
    return true;
  }

  // Update row visibility against the current filter: a single row when given,
  // otherwise every row (followed by a recount).
  function applyFilter(card) {
    const f = currentFilter();
    const filtering =
      !!(f.host || f.script || f.task || f.tags.length) || statusFilterActive(f.statuses);
    filterActive.classList.toggle("hidden", !filtering);
    const update = (c) => setVisible(c, cardMatches(c, f));
    if (card) {
      update(card);
    } else {
      for (const c of cards.values()) update(c);
      recount();
    }
  }

  function addTagToFilter(tag) {
    const have = parseTagFilter(filterEls.tags.value);
    const t = tag.toLowerCase();
    if (!have.includes(t)) filterEls.tags.value = have.concat(t).join(", ");
    applyFilter();
  }

  function rememberTags(tags) {
    let added = false;
    for (const t of tags)
      if (!seenTags.has(t)) {
        seenTags.add(t);
        added = true;
      }
    if (added) renderTagCloud();
  }

  function renderTagCloud() {
    const all = Array.from(seenTags).sort();
    tagCloud.innerHTML = "";
    tagCloud.classList.toggle("hidden", all.length === 0);
    tagCloud.classList.toggle("flex", all.length > 0);
    for (const tag of all) {
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className =
        "inline-flex items-center rounded-full border border-slate-700 bg-slate-800/60 px-2 py-0.5 text-[11px] text-slate-300 transition hover:bg-indigo-500/20 hover:text-indigo-200";
      chip.textContent = tag;
      chip.addEventListener("click", () => addTagToFilter(tag));
      tagCloud.appendChild(chip);
    }
  }

  for (const el of Object.values(filterEls))
    el.addEventListener("input", () => applyFilter());
  for (const box of statusBoxes) box.addEventListener("change", () => applyFilter());
  document.getElementById("filter-clear").addEventListener("click", () => {
    for (const el of Object.values(filterEls)) el.value = "";
    for (const box of statusBoxes) box.checked = box.value === "running";
    applyFilter();
  });

  function connect() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    setStatus("connecting");
    const ws = new WebSocket(proto + "//" + location.host + "/ws");

    ws.onopen = () => setStatus("online");
    ws.onmessage = (ev) => {
      try {
        upsert(JSON.parse(ev.data));
      } catch (e) {
        /* ignore malformed frame */
      }
    };
    ws.onclose = () => {
      setStatus("offline");
      setTimeout(connect, 2000); // auto-reconnect
    };
    ws.onerror = () => ws.close();
  }

  setInterval(sweep, 1000);
  connect();
})();
