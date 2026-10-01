// Dashboard live client. Opens a WebSocket to the server and upserts one foldable
// card per task key (host:description). Unfolding a card reveals its details
// (start time, ETA, rate, …). A task that reaches 100% moves from the "Live
// tasks" section to "Finished tasks". The server only sends updates belonging to
// the logged-in viewer.
(function () {
  "use strict";

  const live = document.getElementById("tasks");
  const liveEmpty = document.getElementById("empty");
  const finished = document.getElementById("finished-tasks");
  const finishedEmpty = document.getElementById("finished-empty");
  const finishedCount = document.getElementById("finished-count");
  const dot = document.getElementById("conn-dot");
  const text = document.getElementById("conn-text");
  const cards = new Map();

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

  function refreshEmpty() {
    liveEmpty.classList.toggle("hidden", live.children.length > 0);
    finishedEmpty.classList.toggle("hidden", finished.children.length > 0);
    finishedCount.textContent = String(finished.children.length);
  }

  function upsert(msg) {
    let card = cards.get(msg.key);
    if (!card) {
      card = buildCard();
      cards.set(msg.key, card);
      live.appendChild(card.el);
    }

    fill(card, msg);

    // Move the card between the Live and Finished sections when its state flips.
    const done = msg.value >= 1;
    if (done && !card.finished) {
      card.finished = true;
      finished.appendChild(card.el);
    } else if (!done && card.finished) {
      card.finished = false;
      live.appendChild(card.el);
    }
    refreshEmpty();
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
