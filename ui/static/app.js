/* Waypoint dashboard: a plain-JS consumer of the public API.
   Everything renders from /api/health + the /api/events SSE stream. */
"use strict";

const $ = (sel) => document.querySelector(sel);

const state = {
  active: null,          // current *_start event, if any
  lastheard: new Map(),  // callsign -> latest end event
  networks: new Map(),   // network name -> state string
};

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
    const r = await fetch("/api/health");
    const h = await r.json();
    $("#pill-version").textContent = "waypointd " + h.version;
    $("#foot-version").textContent = "waypointd " + h.version;
    $("#demo-badge").hidden = !h.demo;
    setConn(true);
  } catch {
    setConn(false);
  }
}

function setConn(up) {
  const p = $("#pill-conn");
  p.dataset.state = up ? "up" : "down";
  p.textContent = up ? "connected" : "disconnected";
}

function setMode(mode) {
  $("#pill-mode").textContent = "mode " + (mode || "—");
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
  box.className = "onair active";
  box.innerHTML =
    `<span class="dir">${dir}</span><div>` +
    `<span class="who">${esc(e.source)}<span class="arrow" aria-hidden="true">→</span>${esc(e.dest)}</span>` +
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
  row.innerHTML = `<td>${fmtTime(e.time)}</td><td><span class="${cls}">${esc(text)}</span></td>`;
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
  es.onerror = () => setConn(false); // EventSource auto-reconnects
  es.onmessage = (m) => handle(JSON.parse(m.data));
}

loadHealth();
connect();
setInterval(renderLastHeard, 15000); // keep "ago" fresh
