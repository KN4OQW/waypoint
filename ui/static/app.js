/* Waypoint dashboard: a plain-JS consumer of the public API. Everything renders
   from /api/health + the /api/events SSE stream. Shares the settings page's
   Nocturne shell and theme (localStorage "wp-theme") so the two read as one app. */
"use strict";

const $ = (sel) => document.querySelector(sel);
const t = (key, params) => WPI18n.t(key, params); // message catalogs — see i18n.js

const state = {
  active: null,          // current *_start event, if any
  lastheard: new Map(),  // callsign -> latest end event
  up: null,              // last known feed state; null until the first answer
  // No networks map: link state comes from /api/status, not a client-side fold.
};

// DMR talkgroup number -> name, for resolving "TG 3112" to "TG 3112 · Texas
// Statewide" inline (RFC-0010 / issue #8). Loaded once from /api/dmr/talkgroups;
// a number not in the map falls back to the raw id, so a missing list degrades to
// bare numbers, never to a blank.
let tgNames = {};
async function loadNames() {
  try {
    const list = await (await fetch("/api/dmr/talkgroups")).json();
    if (Array.isArray(list)) {
      const idx = {};
      for (const tg of list) idx[tg.id] = tg.name;
      tgNames = idx;
      renderOnAir();
      renderLastHeard();
    }
  } catch { /* offline — the dashboard still shows raw TG numbers */ }
}

// tgLabel resolves a DMR group talkgroup destination ("TG 3112") to include its
// name. Only DMR group calls are resolved — a private-call number is a user ID,
// not a talkgroup, and other modes carry reflector names already.
function tgLabel(mode, dest) {
  if (mode !== "DMR" || !dest) return dest;
  const m = /^TG\s*(\d+)$/.exec(dest);
  if (!m) return dest;
  const name = tgNames[m[1]];
  return name ? `${dest} · ${name}` : dest;
}

// Theme is shared with the settings page via localStorage "wp-theme".
const THEMES = [
  { key: "phosphor", color: "#35d07f", attr: "" },
  { key: "amber",    color: "#f0a935", attr: "amber" },
  { key: "ice",      color: "#4db8ff", attr: "ice" },
];
function applyTheme(key) {
  const th = THEMES.find((x) => x.key === key) || THEMES[0];
  if (th.attr) document.documentElement.setAttribute("data-theme", th.attr);
  else document.documentElement.removeAttribute("data-theme");
}
// Dark is the default; "light" is a mode that composes with the accent theme
// (RFC-0009). Persisted separately so both survive a reload.
function currentMode() {
  const m = localStorage.getItem("wp-mode");
  if (m) return m;
  return (window.matchMedia && matchMedia("(prefers-color-scheme: light)").matches) ? "light" : "dark";
}
function applyMode(mode) {
  if (mode === "light") document.documentElement.setAttribute("data-mode", "light");
  else document.documentElement.removeAttribute("data-mode");
}
function renderThemes() {
  const box = $("#swatches");
  box.innerHTML = "";
  const cur = localStorage.getItem("wp-theme") || "phosphor";
  const mode = currentMode();
  // Dark/Light toggle first, then the accent swatches.
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "swatch mode-toggle" + (mode === "light" ? " light" : "");
  toggle.title = mode === "light" ? t("theme.switchToDark") : t("theme.switchToLight");
  toggle.setAttribute("aria-label", t("theme.toggleLight"));
  toggle.setAttribute("aria-pressed", String(mode === "light"));
  toggle.textContent = mode === "light" ? t("theme.light") : t("theme.dark");
  toggle.onclick = () => {
    const next = currentMode() === "light" ? "dark" : "light";
    localStorage.setItem("wp-mode", next);
    applyMode(next);
    renderThemes();
  };
  box.appendChild(toggle);
  THEMES.forEach((th) => {
    const s = document.createElement("button");
    s.type = "button";
    s.className = "swatch" + (th.key === cur ? " on" : "");
    const themeName = t("theme." + th.key);
    s.title = themeName;
    s.setAttribute("aria-label", t("theme.swatchLabel", { theme: themeName }));
    s.setAttribute("aria-pressed", String(th.key === cur));
    s.innerHTML = `<span class="dot" style="background:${th.color}" aria-hidden="true"></span>`;
    s.onclick = () => { applyTheme(th.key); localStorage.setItem("wp-theme", th.key); renderThemes(); };
    box.appendChild(s);
  });
}

function fmtTime(iso) {
  return new Date(iso).toLocaleTimeString(WPI18n.currentLanguage(), { hour12: false });
}
function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso)) / 1000);
  if (s < 60) return t("time.secondsAgo", { n: s.toFixed(0) });
  if (s < 3600) return t("time.minutesAgo", { n: (s / 60).toFixed(0) });
  return t("time.hoursAgo", { n: (s / 3600).toFixed(1) });
}

async function loadHealth() {
  try {
    const h = await (await fetch("/api/health")).json();
    $("#st-version").textContent = h.version;
    $("#foot-version").textContent = t("foot.version", { version: h.version });
    $("#st-feed").textContent = h.demo ? t("status.feedDemo") : t("status.feedLive");
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
  state.up = up; // remembered so a language change can re-render it without a poll
  $("#conn-led").className = "conn-led " + (up ? "up" : "down");
  $("#conn-txt").textContent = up ? t("status.connected") : t("status.disconnected");
  $("#side-led").className = "led" + (up ? "" : " down");
  $("#side-online").textContent = up ? t("sidebar.online") : t("sidebar.offline");
}

function setMode(mode) {
  $("#st-mode").textContent = mode || "—";
}

function renderOnAir() {
  const box = $("#onair");
  const e = state.active;
  if (!e) {
    box.className = "onair idle";
    box.innerHTML = `<p class="onair-idle">${esc(t("dash.onair.idle"))}</p>`;
    return;
  }
  const dir = e.type === "rf_voice_start" ? "RF" : "NET";
  const dirWord = dir === "RF" ? t("dash.onair.rf") : t("dash.onair.net");
  const meta = [esc(e.mode)];
  if (e.slot) meta.push(esc(t("dash.onair.slot", { slot: e.slot })));
  box.className = "onair active";
  box.innerHTML =
    `<span class="dir"><span aria-hidden="true">${dir}</span><span class="sr-only">${esc(dirWord)}</span></span><div>` +
    `<span class="who">${esc(e.source)}<span class="arrow" aria-hidden="true">→</span><span class="sr-only">${esc(t("dash.onair.to"))}</span>${esc(tgLabel(e.mode, e.dest))}</span>` +
    `<span class="meta">${meta.join(" ")}${e.network ? " · " + esc(e.network) : ""}</span>` +
    `</div>`;
}

function renderLastHeard() {
  const rows = [...state.lastheard.values()]
    .sort((a, b) => new Date(b.time) - new Date(a.time))
    .slice(0, 12);
  $("#lastheard-empty").hidden = rows.length > 0;
  $("#lastheard tbody").innerHTML = rows.map((e) =>
    `<tr><td><span class="call">${esc(e.source)}</span></td><td>${esc(tgLabel(e.mode, e.dest))}</td>` +
    `<td>${esc(e.mode)}${e.slot ? esc(t("dash.lastheard.slot", { slot: e.slot })) : ""}</td>` +
    `<td class="num">${e.seconds ? esc(t("dash.lastheard.seconds", { n: e.seconds.toFixed(1) })) : "—"}</td>` +
    `<td class="num">${e.ber != null && e.type === "rf_voice_end" ? esc(t("dash.lastheard.berValue", { n: e.ber.toFixed(1) })) : "—"}</td>` +
    `<td class="num">${ago(e.time)}</td></tr>`
  ).join("");
}

// Network links render from the server's computed status, exactly like gateways —
// NOT from a client-side fold of the event stream. The old fold could only ever
// add a network and always drew it with a ✓, so a link that dropped stayed green
// forever and a reloaded tab replayed an ancient "link" event out of history and
// believed it. One server-side truth, self-healing, is the whole point of the
// Link state comes from linkstate.js (WPLink), which is where the three-state
// read and its fallbacks live so ui/tests can drive them without a DOM.
const linkClass = (l) => WPLink.linkClass(l);
const linkMark = (l) => WPLink.linkMark(l);
const linkWord = (l) => WPLink.linkWord(l);

function gatewayWord(g) {
  const s = WPLink.linkState(g);
  if (s === WPLink.UNKNOWN) return t("dash.gateways.unknown");
  return s === WPLink.UP ? t("dash.gateways.running") : t("dash.gateways.stopped");
}

// status pipeline (RFC-0008); the event stream's job here is the log, not state.
function renderNetworks(nets) {
  const items = Object.entries(nets || {}).sort((a, b) => a[0].localeCompare(b[0]));
  $("#networks-empty").hidden = items.length > 0;
  $("#networks").innerHTML = items.map(([name, l]) =>
    `<li class="${linkClass(l)}"><span class="dot" aria-hidden="true"></span>${esc(name)}` +
    `<span class="state">${esc(l.detail || linkWord(l))} ${linkMark(l)}</span></li>`
  ).join("");
}

// A link event reads the same with or without the daemon's detail line, so the
// two phrasings are one key pair and the caller only names the event.
function linkText(key, e) {
  return e.detail ? t(key + "Detail", { network: e.network, detail: e.detail }) : t(key, { network: e.network });
}

function logEvent(e) {
  const tbody = $("#eventlog tbody");
  const cls = e.type.startsWith("rf") ? "ev-rf" : e.type.startsWith("net") ? "ev-net" : "";
  const dest = tgLabel(e.mode, e.dest); // resolve DMR TG numbers to names inline
  let text;
  const slot = e.slot ? t("log.slot", { slot: e.slot }) : "";
  switch (e.type) {
    case "rf_voice_start": text = t("log.rfStart", { source: e.source, dest, mode: e.mode, slot }); break;
    case "rf_voice_end":   text = t("log.rfEnd", { source: e.source, dest, seconds: e.seconds, ber: e.ber, rssi: e.rssi }); break;
    case "net_voice_start":text = t("log.netStart", { source: e.source, dest, network: e.network }); break;
    case "net_voice_end":  text = t("log.netEnd", { source: e.source, dest, seconds: e.seconds }); break;
    case "link":
    case "link_up":        text = linkText("log.linkUp", e); break;
    case "link_down":      text = linkText("log.linkDown", e); break;
    case "link_removed":   text = linkText("log.linkRemoved", e); break;
    case "gateway_status": text = t("log.gatewayStatus", { network: e.network, detail: e.detail }); break;
    case "supervisor_action": text = t("log.supervisorAction", { detail: e.detail }); break;
    case "mode":           text = e.detail ? t("log.modeDetail", { mode: e.mode, detail: e.detail }) : t("log.mode", { mode: e.mode }); break;
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
    case "mode":
      setMode(e.mode); break;
  }
  renderOnAir();
  renderLastHeard();
  logEvent(e);
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// loadHistory seeds the dashboard from the server's persistent event record
// (RFC-0004) before the live stream attaches, so a freshly-opened tab renders the
// same history as every other client and survives daemon restarts — the fix for
// the old per-browser-session history (#68). Best-effort: if it fails the live SSE
// stream still populates the dashboard going forward.
async function loadHistory() {
  try {
    const r = await fetch("/api/history?limit=500");
    if (!r.ok) return;
    const events = await r.json();
    if (!Array.isArray(events)) return;
    // The server returns newest-first; replay oldest-first so the event log prepends
    // into newest-first order and lastheard settles on its latest value, exactly as
    // the live stream would have built it. Link state is deliberately NOT rebuilt
    // from here — a link event in the record is history, not a claim about now.
    for (let i = events.length - 1; i >= 0; i--) handle(events[i]);
    // History must not imply a live transmission: a trailing voice_start in the
    // record doesn't mean someone is keyed up right now. Clear on-air and let the
    // live stream set it.
    state.active = null;
    renderOnAir();
  } catch {
    /* history is best-effort; the live SSE stream still populates the dashboard */
  }
}

function connect() {
  const es = new EventSource("/api/events");
  es.onerror = () => {}; // EventSource auto-reconnects; the feed LED comes from /api/status
  es.onmessage = (m) => handle(JSON.parse(m.data));
}

// loadStatus polls the server-computed live status (RFC-0008). This is the
// authoritative, self-healing truth — the connection LED reflects the actual
// MMDVM-Host feed (not just an open SSE socket), a stranded transmission that
// never got a closing event self-clears here, and gateway liveness comes from the
// supervisor. The SSE stream still drives the event log and last-heard for
// immediacy; the poll corrects any state the raw stream can't (the #5 fix).
function renderGateways(gws) {
  const items = Object.entries(gws || {}).sort((a, b) => a[0].localeCompare(b[0]));
  $("#gateways-empty").hidden = items.length > 0;
  $("#gateways").innerHTML = items.map(([name, g]) =>
    `<li class="${linkClass(g)}"><span class="dot" aria-hidden="true"></span>${esc(name)}` +
    `<span class="state">${esc(g.detail || gatewayWord(g))}</span></li>`
  ).join("");
}

async function loadStatus() {
  let s;
  try {
    s = await (await fetch("/api/status")).json();
  } catch {
    setConn(false);
    return;
  }
  setConn(!!(s.feed && s.feed.connected));
  setMode(s.mode);
  // On-air is authoritative from the server (self-heals a stranded transmission).
  if (s.tx) {
    state.active = {
      type: s.tx.direction === "network" ? "net_voice_start" : "rf_voice_start",
      source: s.tx.source, dest: s.tx.dest, mode: s.tx.mode, slot: s.tx.slot, network: s.tx.network,
    };
  } else {
    state.active = null;
  }
  renderOnAir();
  renderGateways(s.gateways);
  renderNetworks(s.networks);
}

// The language picker sits under the theme swatches, and is populated from the
// catalog index rather than a list in here.
function mountLanguagePicker() { WPI18n.renderPicker($("#lang-pick")); }

// i18n.js re-applies the static data-i18n markup on a language change; what it
// cannot know is which of those elements JavaScript has since overwritten with
// live state. #conn-txt and #side-online carry their *placeholder* text as
// data-i18n, so a language change resets them to "connecting…" — re-assert the
// state we actually last saw instead of waiting out the 2s poll.
addEventListener("wp-lang-changed", () => {
  mountLanguagePicker();
  renderThemes(); // the swatches carry titles and aria-labels
  if (state.up !== null) setConn(state.up);
  renderOnAir();
  loadHealth(); // the footer's "waypointd {version}" is interpolated, not markup
});

// Theme and mode are pure CSS attributes, and the inline script in the page head
// already applied them before first paint, so they do not wait on anything. The
// swatch UI itself does: it carries labels.
applyMode(currentMode());
applyTheme(localStorage.getItem("wp-theme") || "phosphor");

// Everything below renders text, so it waits for the catalogs. Starting it
// earlier would lose a race twice over: t() would answer with bare keys before
// the fetch landed, and i18n.js re-applying the static markup afterwards would
// stamp the placeholder "connecting…" back over a status that had already
// resolved. WPI18n.ready never rejects — a missing catalog degrades to English
// inside i18n.js — so this is a delay of one same-origin fetch, not a new way
// for the dashboard to fail to load.
WPI18n.ready.then(() => {
  renderThemes();
  mountLanguagePicker();
  loadHealth();
  loadCallsign();
  loadNames(); // DMR talkgroup names, for inline resolution (RFC-0010)
  loadHistory().then(connect); // seed persistent history, then attach the live tail
  loadStatus(); // server-computed truth (feed, on-air self-heal, gateways)
  setInterval(loadStatus, 2000); // reflect gateway kill/restart within the #5 window
  setInterval(renderLastHeard, 15000); // keep "ago" fresh
});
