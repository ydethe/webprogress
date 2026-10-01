// Dashboard live client. Opens a WebSocket to the server and upserts one foldable
// card per task key (script:host:description). Tasks are grouped on screen under
// their deployable (the host/user running them), and within a deployable under
// the script: Deployable ▸ Script ▸ task cards. Unfolding a card reveals its
// details (start time, ETA, rate, …). Every task lives in a single "Tasks"
// section and carries a status badge — running, finished, stalled, or dead —
// derived from its progress and how long it has gone silent relative to how
// often it normally reports; a status filter (running only by default) decides
// which are shown. The server only sends updates belonging to the logged-in
// viewer.
(function () {
  "use strict";

  const dot = document.getElementById("conn-dot");
  const text = document.getElementById("conn-text");
  const empty = document.getElementById("empty");
  const taskCount = document.getElementById("task-count");

  // Cards are grouped under Deployable ▸ Script. `deployables` maps a deployable
  // id to its group; each group maps a script name to its subgroup.
  const section = { root: document.getElementById("tasks"), deployables: new Map() };

  const cards = new Map(); // task key -> card (carrying its current location & status)

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

  const CHEVRON =
    '<svg class="h-4 w-4 shrink-0 text-slate-500 transition-transform" viewBox="0 0 20 20" fill="currentColor"><path d="M7 5l6 5-6 5V5z"/></svg>';

  // ---- status ----------------------------------------------------------------

  // The four lifecycle states, mirroring models.TaskStatus on the server. Each
  // maps to the badge classes and label shown on a card.
  const STATUS_STYLES = {
    running: ["bg-emerald-500/15 text-emerald-300", "running"],
    finished: ["bg-sky-500/15 text-sky-300", "finished"],
    stalled: ["bg-amber-500/15 text-amber-300", "stalled"],
    dead: ["bg-rose-500/15 text-rose-300", "dead"],
  };
  const BADGE_BASE =
    "status shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums ";

  // Derive a card's current status from its latest frame and how long it has
  // been silent. A finished task (value ≥ 1) stays finished; a task that has gone
  // quiet for longer than the server's stall/dead thresholds (derived from how
  // often it normally reports) goes stalled then dead. While those thresholds are
  // zero (cadence not yet known) the task can only be running or finished.
  function cardStatus(card) {
    if (card.value >= 1) return "finished";
    const idle = (Date.now() - card.lastSeen) / 1000;
    if (card.deadSeconds > 0 && idle >= card.deadSeconds) return "dead";
    if (card.stallSeconds > 0 && idle >= card.stallSeconds) return "stalled";
    return "running";
  }

  // Recompute and render a card's status badge; returns true when it changed.
  function refreshStatus(card) {
    const s = cardStatus(card);
    if (s === card.status) return false;
    card.status = s;
    const [cls, label] = STATUS_STYLES[s];
    card.statusEl.className = BADGE_BASE + cls;
    card.statusEl.textContent = label;
    return true;
  }

  // ---- group containers ------------------------------------------------------

  // A deployable is one (host, login) running a script; src is shown as a hint.
  function deployableId(msg) {
    return (msg.host || "—") + "\u0000" + (msg.login || "—");
  }

  function ensureDeployableGroup(msg) {
    const id = deployableId(msg);
    let g = section.deployables.get(id);
    if (g) return g;

    const el = document.createElement("details");
    el.open = true;
    el.className = "group/dep rounded-xl border border-slate-800/80 bg-slate-900/20";
    el.innerHTML =
      '<summary class="flex items-center gap-2 cursor-pointer select-none list-none px-4 py-3">' +
      CHEVRON.replace("transition-transform", "transition-transform group-open/dep:rotate-90") +
      '  <span class="host text-sm font-medium"></span>' +
      '  <span class="meta text-xs text-slate-500"></span>' +
      '  <span class="count ml-auto rounded-full bg-slate-800 px-2 py-0.5 text-xs tabular-nums text-slate-400">0</span>' +
      "</summary>" +
      '<div class="scripts space-y-3 px-4 pb-4"></div>';
    el.querySelector(".host").textContent = msg.host || "—";
    const meta = [msg.login, msg.src_address].filter(Boolean).join(" · ");
    el.querySelector(".meta").textContent = meta;

    g = {
      el: el,
      body: el.querySelector(".scripts"),
      count: el.querySelector(".count"),
      scripts: new Map(),
    };
    section.deployables.set(id, g);
    section.root.appendChild(el);
    return g;
  }

  function ensureScript(group, name) {
    let s = group.scripts.get(name);
    if (s) return s;

    const el = document.createElement("details");
    el.open = true;
    el.className = "group/script rounded-lg border border-slate-800/60 bg-slate-900/30";
    el.innerHTML =
      '<summary class="flex items-center gap-2 cursor-pointer select-none list-none px-3 py-2">' +
      CHEVRON.replace("transition-transform", "transition-transform group-open/script:rotate-90") +
      '  <span class="name text-sm text-slate-300"></span>' +
      '  <span class="count ml-auto rounded-full bg-slate-800 px-2 py-0.5 text-xs tabular-nums text-slate-400">0</span>' +
      "</summary>" +
      '<div class="tasks grid gap-4 sm:grid-cols-2 px-3 pb-3"></div>';
    el.querySelector(".name").textContent = name;

    s = {
      el: el,
      body: el.querySelector(".tasks"),
      count: el.querySelector(".count"),
    };
    group.scripts.set(name, s);
    group.body.appendChild(el);
    return s;
  }

  function placeCard(card, msg) {
    const group = ensureDeployableGroup(msg);
    const script = ensureScript(group, msg.script || "(unscripted)");
    script.body.appendChild(card.el);
    card.loc = { dep: deployableId(msg), script: msg.script || "(unscripted)" };
  }

  function removeCard(card) {
    if (!card.loc) return;
    const group = section.deployables.get(card.loc.dep);
    if (!group) return;
    const script = group.scripts.get(card.loc.script);
    if (script) {
      card.el.remove();
      if (script.body.children.length === 0) {
        script.el.remove();
        group.scripts.delete(card.loc.script);
      }
      if (group.scripts.size === 0) {
        group.el.remove();
        section.deployables.delete(card.loc.dep);
      }
    }
    card.loc = null;
  }

  // Count how many of a container's child cards are currently visible (not
  // hidden by the filter).
  function visibleCount(el) {
    let n = 0;
    for (const child of el.children) if (!child.classList.contains("hidden")) n++;
    return n;
  }

  function recount() {
    let total = 0;
    for (const group of section.deployables.values()) {
      let groupVisible = 0;
      for (const script of group.scripts.values()) {
        const n = visibleCount(script.body);
        script.count.textContent = String(n);
        script.el.classList.toggle("hidden", n === 0);
        groupVisible += n;
      }
      group.count.textContent = String(groupVisible);
      group.el.classList.toggle("hidden", groupVisible === 0);
      total += groupVisible;
    }
    empty.classList.toggle("hidden", total > 0);
    taskCount.textContent = String(total);
  }

  // ---- task cards ------------------------------------------------------------

  // The detail rows revealed when a card is unfolded, in display order. Each key
  // matches a data-f attribute set up in buildCard and updated in fill.
  const FIELDS = [
    ["started", "Started"],
    ["eta", "ETA"],
    ["elapsed", "Elapsed"],
    ["remaining", "Remaining"],
    ["progress", "Progress"],
    ["rate", "Rate"],
    ["host", "Host"],
    ["login", "User"],
    ["src", "Source"],
  ];

  function buildCard() {
    const el = document.createElement("details");
    el.className = "group/card rounded-xl border border-slate-800 bg-slate-900/40 p-4";
    el.innerHTML =
      '<summary class="list-none cursor-pointer select-none">' +
      '  <div class="flex items-center justify-between gap-2 mb-2">' +
      '    <p class="truncate text-sm font-medium"><span class="chev inline-block text-slate-500 transition-transform group-open/card:rotate-90">▸</span> <span class="name"></span></p>' +
      '    <span class="flex shrink-0 items-center gap-2">' +
      '      <span class="status shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums"></span>' +
      '      <span class="pct text-xs tabular-nums text-slate-400"></span>' +
      "    </span>" +
      "  </div>" +
      '  <div class="h-2.5 w-full overflow-hidden rounded-full bg-slate-800">' +
      '    <div class="bar h-full rounded-full transition-[width] duration-300 ease-out" style="width:0%"></div>' +
      "  </div>" +
      '  <div class="tags mt-2 hidden flex-wrap gap-1"></div>' +
      "</summary>" +
      '<dl class="mt-3 text-xs"></dl>';
    const dl = el.querySelector("dl");
    for (const [key, label] of FIELDS) {
      dl.innerHTML +=
        '<div class="flex justify-between gap-3 border-b border-slate-800/60 py-1 last:border-0">' +
        '  <dt class="text-slate-500">' + label + "</dt>" +
        '  <dd class="truncate text-right text-slate-300" data-f="' + key + '">—</dd>' +
        "</div>";
    }
    const fields = {};
    el.querySelectorAll("[data-f]").forEach((n) => (fields[n.dataset.f] = n));
    return {
      el: el,
      name: el.querySelector(".name"),
      bar: el.querySelector(".bar"),
      pct: el.querySelector(".pct"),
      statusEl: el.querySelector(".status"),
      tags: el.querySelector(".tags"),
      fields: fields,
      status: null,
      value: 0,
      lastSeen: 0,
      stallSeconds: 0,
      deadSeconds: 0,
      loc: null,
      meta: { host: "", login: "", script: "", description: "", tags: [] },
    };
  }

  // Render a task's tags as chips. Clicking a chip adds it to the Tags filter.
  function renderTags(card, tags) {
    const list = Array.isArray(tags) ? tags.filter(Boolean) : [];
    card.tags.innerHTML = "";
    card.tags.classList.toggle("hidden", list.length === 0);
    card.tags.classList.toggle("flex", list.length > 0);
    for (const tag of list) {
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className =
        "chip inline-flex items-center rounded-full bg-slate-800 px-2 py-0.5 text-[11px] text-slate-300 transition hover:bg-indigo-500/20 hover:text-indigo-200";
      chip.textContent = tag;
      chip.addEventListener("click", (e) => {
        e.preventDefault();
        addTagToFilter(tag);
      });
      card.tags.appendChild(chip);
    }
  }

  function fill(card, msg) {
    card.name.textContent = msg.label;

    const pct = Math.max(0, Math.min(1, msg.value)) * 100;
    card.bar.style.width = pct.toFixed(1) + "%";
    card.bar.style.backgroundColor = cssColour(msg.colour);
    card.pct.textContent = pct.toFixed(0) + "%";

    // Liveness bookkeeping: remember this frame's arrival and the silence
    // thresholds so the sweep can later age the task into stalled/dead.
    card.value = msg.value;
    card.lastSeen = Date.now();
    card.stallSeconds = typeof msg.stall_seconds === "number" ? msg.stall_seconds : 0;
    card.deadSeconds = typeof msg.dead_seconds === "number" ? msg.dead_seconds : 0;

    const isDone = msg.value >= 1;
    const started = typeof msg.elapsed === "number"
      ? new Date(Date.now() - msg.elapsed * 1000)
      : null;
    const eta = isDone ? null : (msg.eta ? new Date(msg.eta) : null);
    const unit = msg.unit || "it";

    const f = card.fields;
    f.started.textContent = fmtDate(started);
    f.eta.textContent = isDone ? "done" : fmtDate(eta);
    f.elapsed.textContent = fmtDuration(msg.elapsed);
    f.remaining.textContent = isDone ? "—" : fmtDuration(msg.remaining);
    f.progress.textContent = fmtNum(msg.progress) + " / " + fmtNum(msg.total) + " " + unit;
    f.rate.textContent =
      typeof msg.rate === "number" && msg.rate > 0 ? fmtNum(msg.rate) + " " + unit + "/s" : "—";
    f.host.textContent = msg.host || "—";
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

  function upsert(msg) {
    let card = cards.get(msg.key);
    if (!card) {
      card = buildCard();
      cards.set(msg.key, card);
    }

    fill(card, msg);
    refreshStatus(card);

    // Re-place the card when its grouping (deployable/script) changes.
    const targetLoc = { script: msg.script || "(unscripted)", dep: deployableId(msg) };
    const moved =
      !card.loc || card.loc.script !== targetLoc.script || card.loc.dep !== targetLoc.dep;
    if (moved) {
      removeCard(card);
      placeCard(card, msg);
    }
    applyFilter(card);
    recount();
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

  // A card matches when every set text filter is a (case-insensitive) substring
  // of the corresponding field, every requested tag is present, and its current
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

  // Update card visibility against the current filter: a single card when given,
  // otherwise every card (followed by a recount to hide emptied groups).
  function applyFilter(card) {
    const f = currentFilter();
    const filtering =
      !!(f.host || f.script || f.task || f.tags.length) || statusFilterActive(f.statuses);
    filterActive.classList.toggle("hidden", !filtering);
    const update = (c) => c.el.classList.toggle("hidden", !cardMatches(c, f));
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
