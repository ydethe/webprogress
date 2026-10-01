// Dashboard live client. Opens a WebSocket to the server and upserts one foldable
// card per task key (script:host:description). Tasks are grouped on screen under
// their deployable (the host/user running them), and within a deployable under
// the script: Deployable ▸ Script ▸ task cards. Unfolding a card reveals its
// details (start time, ETA, rate, …). A task that reaches 100% moves from the
// "Live tasks" section to "Finished tasks". The server only sends updates
// belonging to the logged-in viewer.
(function () {
  "use strict";

  const dot = document.getElementById("conn-dot");
  const text = document.getElementById("conn-text");
  const liveEmpty = document.getElementById("empty");
  const finishedEmpty = document.getElementById("finished-empty");
  const finishedCount = document.getElementById("finished-count");

  // Each section groups cards under Deployable ▸ Script. `deployables` maps a
  // deployable id to its group; each group maps a script name to its subgroup.
  const sections = {
    live: { root: document.getElementById("tasks"), deployables: new Map() },
    finished: { root: document.getElementById("finished-tasks"), deployables: new Map() },
  };

  const cards = new Map(); // task key -> card (carrying its current location)

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

  // ---- group containers ------------------------------------------------------

  // A deployable is one (host, login) running a script; src is shown as a hint.
  function deployableId(msg) {
    return (msg.host || "—") + "\u0000" + (msg.login || "—");
  }

  function ensureDeployableGroup(section, msg) {
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

  function placeCard(sectionName, card, msg) {
    const section = sections[sectionName];
    const group = ensureDeployableGroup(section, msg);
    const script = ensureScript(group, msg.script || "(unscripted)");
    script.body.appendChild(card.el);
    card.loc = { section: sectionName, dep: deployableId(msg), script: msg.script || "(unscripted)" };
  }

  function removeCard(card) {
    if (!card.loc) return;
    const section = sections[card.loc.section];
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

  function recount() {
    let finishedTotal = 0;
    for (const sec of Object.values(sections)) {
      for (const group of sec.deployables.values()) {
        let total = 0;
        for (const script of group.scripts.values()) {
          const n = script.body.children.length;
          script.count.textContent = String(n);
          total += n;
        }
        group.count.textContent = String(total);
      }
    }
    for (const group of sections.finished.deployables.values())
      for (const script of group.scripts.values())
        finishedTotal += script.body.children.length;

    liveEmpty.classList.toggle("hidden", sections.live.deployables.size > 0);
    finishedEmpty.classList.toggle("hidden", sections.finished.deployables.size > 0);
    finishedCount.textContent = String(finishedTotal);
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
      '  <div class="flex items-center justify-between mb-2">' +
      '    <p class="truncate text-sm font-medium"><span class="chev inline-block text-slate-500 transition-transform group-open/card:rotate-90">▸</span> <span class="name"></span></p>' +
      '    <span class="pct text-xs tabular-nums text-slate-400"></span>' +
      "  </div>" +
      '  <div class="h-2.5 w-full overflow-hidden rounded-full bg-slate-800">' +
      '    <div class="bar h-full rounded-full transition-[width] duration-300 ease-out" style="width:0%"></div>' +
      "  </div>" +
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
      fields: fields,
      finished: false,
      loc: null,
    };
  }

  function fill(card, msg) {
    card.name.textContent = msg.label;

    const pct = Math.max(0, Math.min(1, msg.value)) * 100;
    card.bar.style.width = pct.toFixed(1) + "%";
    card.bar.style.backgroundColor = cssColour(msg.colour);
    card.pct.textContent = pct.toFixed(0) + "%";

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
  }

  function upsert(msg) {
    let card = cards.get(msg.key);
    if (!card) {
      card = buildCard();
      cards.set(msg.key, card);
    }

    fill(card, msg);

    // Place the card in the right section (live vs finished), moving it — and the
    // script/deployable groups around it — when its state or grouping changes.
    const done = msg.value >= 1;
    const targetSection = done ? "finished" : "live";
    const targetLoc = { section: targetSection, script: msg.script || "(unscripted)", dep: deployableId(msg) };
    const moved =
      !card.loc ||
      card.loc.section !== targetLoc.section ||
      card.loc.script !== targetLoc.script ||
      card.loc.dep !== targetLoc.dep;
    if (moved) {
      removeCard(card);
      placeCard(targetSection, card, msg);
    }
    card.finished = done;
    recount();
  }

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

  connect();
})();
