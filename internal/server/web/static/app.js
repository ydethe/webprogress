// Dashboard live client. Opens a WebSocket to the server and upserts one card
// with an animated bar per task key (host:description). The server only sends
// updates belonging to the logged-in viewer.
(function () {
  "use strict";

  const tasks = document.getElementById("tasks");
  const empty = document.getElementById("empty");
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

  function upsert(msg) {
    empty.classList.add("hidden");

    let card = cards.get(msg.key);
    if (!card) {
      const el = document.createElement("div");
      el.className = "rounded-xl border border-slate-800 bg-slate-900/40 p-4";
      el.innerHTML =
        '<div class="flex items-center justify-between mb-2">' +
        '  <p class="truncate text-sm font-medium"></p>' +
        '  <span class="pct text-xs tabular-nums text-slate-400"></span>' +
        "</div>" +
        '<div class="h-2.5 w-full overflow-hidden rounded-full bg-slate-800">' +
        '  <div class="bar h-full rounded-full transition-[width] duration-300 ease-out" style="width:0%"></div>' +
        "</div>";
      el.querySelector("p").textContent = msg.label;
      tasks.appendChild(el);
      card = {
        bar: el.querySelector(".bar"),
        pct: el.querySelector(".pct"),
      };
      cards.set(msg.key, card);
    }

    const pct = Math.max(0, Math.min(1, msg.value)) * 100;
    card.bar.style.width = pct.toFixed(1) + "%";
    card.bar.style.backgroundColor = cssColour(msg.colour);
    card.pct.textContent = pct.toFixed(0) + "%";
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
