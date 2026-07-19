/* Waypoint dashboard: a plain-JS consumer of the public API. Everything renders
   from /api/health + the /api/events SSE stream. Shares the settings page's
   Nocturne shell and theme (localStorage "wp-theme") so the two read as one app. */
"use strict";

const $ = (sel) => document.querySelector(sel);

const state = {
  active: null,          // current *_start event, if any
  lastheard: new Map(),  // callsign -> latest end event
  networks: new Map(),   // network name -> state string
};

// Theme is shared with the settings page via localStorage "wp-theme".
const THEMES = [
  { key: "phosphor", color: "#35d07f", attr: "" },
  { key: "amber",    color: "#f0a935", attr: "amber" },
  { key: "ice",      color: "#4db8ff", attr: "ice" },
];
function applyTheme(key) {
  const th = THEMES.find((t) => t.key === key) || THEMES[0];
  if (th.attr) document.documentElement.setAttribute("data-theme", th.attr);
  else document.documentElement.removeAttribute("data-theme");
}
function renderThemes() {
  const box = $("#swatches");
  box.innerHTML = "";
  const cur = localStorage.getItem("wp-theme") || "phosphor";
  THEMES.forEach((th) => {
    const s = document.createElement("button");
    s.type = "button";
    s.className = "swatch" + (th.key === cur ? " on" : "");
    s.title = th.key;
    s.setAttribute("aria-label", th.key + " theme");
    s.setAttribute("aria-pressed", String(th.key === cur));
    s.innerHTML = `<span class="dot" style="background:${th.color}" aria-hidden="true"></span>`;
    s.onclick = () => { applyTheme(th.key); localStorage.setItem("wp-theme", th.key); renderThemes(); };
    box.appendChild(s);
  });
}

function fmtTime(iso) {
  return new Date(iso).toLocaleTimeString([], { hour12: false });
}
function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
  if (s < 60) return `${s.toFixed(0)}s ago`;
  if (s < 3600) return `${(s / 60).toFixed(0)}m ago`;
  return `${(s / 3600).toFixed(1)}h ago`;
}

async function loadHealth() {
  try {
    const h = await (await fetch("/api/health")).json();
    $("#st-version").textContent = h.version;
    $("#foot-version").textContent = "waypointd " + h.version;
    $("#st-feed").textContent = h.demo ? "demo" : "live";
    $("#demo-badge").hidden = !h.demo;
    setConn(true);
  } catch {
    setConn(false);
  }
}

// The callsign chip mirrors the settings sidebar; sourced from the config API.
async function loadCallsign() {
  try {
    const c = await (await fetch("/api/config")).json();
    const cs = (c.general && c.general.callsign) || "";
    if (cs) $("#side-callsign").textContent = cs;
  } catch { /* offline — leave the placeholder */ }
}

function setConn(up) {
  $("#conn-led").className = "conn-led " + (up ? "up" : "down");
  $("#conn-txt").textContent = up ? "connected" : "disconnected";
  $("#side-led").className = "led" + (up ? "" : " down");
  $("#side-online").textContent = up ? "ONLINE" : "OFFLINE";
}

function setMode(mode) {
  $("#st-mode").textContent = mode || "—";
}

function renderOnAir() {
  const box = $("#onair");
  const e = state.active;
  if (!e) {
    box.className = "onair idle";
    box.innerHTML = '<p class="onair-idle">Listening — no active transmission</p>';
    return;
  }
  const dir = e.type === "rf_voice_start" ? "RF" : "NET";
  const dirWord = dir === "RF" ? "RF transmission" : "Network transmission";
  box.className = "onair active";
  box.innerHTML =
    `<span class="dir"><span aria-hidden="true">${dir}</span><span class="sr-only">${dirWord}</span></span><div>` +
    `<span class="who">${esc(e.source)}<span class="arrow" aria-hidden="true">→</span><span class="sr-only"> to </span>${esc(e.dest)}</span>` +
    `<span class="meta">${esc(e.mode)}${e.slot ? " slot " + e.slot : ""}${e.network ? " · " + esc(e.network) : ""}</span>` +
    `</div>`;
}

function renderLastHeard() {
  const rows = [...state.lastheard.values()]
    .sort((a, b) => new Date(b.time) - new Date(a.time))
    .slice(0, 12);
  $("#lastheard-empty").hidden = rows.length > 0;
  $("#lastheard tbody").innerHTML = rows.map((e) =>
    `<tr><td><span class="call">${esc(e.source)}</span></td><td>${esc(e.dest)}</td>` +
    `<td>${esc(e.mode)}${e.slot ? "·S" + e.slot : ""}</td>` +
    `<td class="num">${e.seconds ? e.seconds.toFixed(1) + "s" : "—"}</td>` +
    `<td class="num">${e.ber != null && e.type === "rf_voice_end" ? e.ber.toFixed(1) + "%" : "—"}</td>` +
    `<td class="num">${ago(e.time)}</td></tr>`
  ).join("");
}

function renderNetworks() {
  const items = [...state.networks.entries()];
  $("#networks-empty").hidden = items.length > 0;
  $("#networks").innerHTML = items.map(([name, st]) =>
    `<li><span class="dot" aria-hidden="true"></span>${esc(name)}<span class="state">${esc(st)} ✓</span></li>`
  ).join("");
}

function logEvent(e) {
  const tbody = $("#eventlog tbody");
  const cls = e.type.startsWith("rf") ? "ev-rf" : e.type.startsWith("net") ? "ev-net" : "";
  let text;
  switch (e.type) {
    case "rf_voice_start": text = `${e.source} keyed up → ${e.dest} (${e.mode}${e.slot ? " S" + e.slot : ""})`; break;
    case "rf_voice_end":   text = `${e.source} → ${e.dest}, ${e.seconds}s, BER ${e.ber}%, RSSI ${e.rssi} dBm`; break;
    case "net_voice_start":text = `${e.source} → ${e.dest} from ${e.network}`; break;
    case "net_voice_end":  text = `${e.source} → ${e.dest}, ${e.seconds}s (network)`; break;
    case "link":           text = `${e.network}: ${e.detail}`; break;
    case "mode":           text = `mode ${e.mode}${e.detail ? " — " + e.detail : ""}`; break;
    default:               text = e.detail || e.type;
  }
  const row = document.createElement("tr");
  row.innerHTML = `<td class="num" style="text-align:left">${fmtTime(e.time)}</td><td><span class="${cls}">${esc(text)}</span></td>`;
  tbody.prepend(row);
  while (tbody.children.length > 100) tbody.lastChild.remove();
}

function handle(e) {
  switch (e.type) {
    case "rf_voice_start":
    case "net_voice_start":
      state.active = e; break;
    case "rf_voice_end":
    case "net_voice_end":
      state.active = null;
      state.lastheard.set(e.source, e);
      break;
    case "link":
      state.networks.set(e.network, e.detail || "linked"); break;
    case "mode":
      setMode(e.mode); break;
  }
  renderOnAir();
  renderLastHeard();
  renderNetworks();
  logEvent(e);
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function connect() {
  const es = new EventSource("/api/events");
  es.onopen = () => setConn(true);
  es.onerror = () => {
    // EventSource hides the HTTP status and just retries, so a session that
    // expired mid-stream would otherwise leave a silently-frozen dashboard. Probe a
    // gated route: if the session is gone the shared client routes to the login
    // screen; a transient blip is left to the automatic reconnect (RFC-0002).
    setConn(false);
    if (window.wpSession) window.wpSession.reauthCheck();
  };
  es.onmessage = (m) => handle(JSON.parse(m.data));
}

applyTheme(localStorage.getItem("wp-theme") || "phosphor");
renderThemes();
loadHealth();
loadCallsign();
connect();
setInterval(renderLastHeard, 15000); // keep "ago" fresh
