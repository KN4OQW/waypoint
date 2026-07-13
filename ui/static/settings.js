// Waypoint settings page. Every value shown is read live from /api/config and
// /api/health — nothing is hard-coded. The page is read-only for now: the write
// path (regenerate INI, validate, restart) lands with the configuration store.

const TABS = [
  { id: "general",      tag: "RF", label: "General",      sub: "Radio & Station",     crumb: "SYSTEM / GENERAL",        title: "General Configuration", desc: "Station identity, operating frequencies and modem hardware for this hotspot node." },
  { id: "brandmeister", tag: "BM", label: "BrandMeister", sub: "Network & Security",   crumb: "NETWORKS / BRANDMEISTER", title: "DMR Networks",          desc: "Master servers this node bridges DMR traffic to. Passwords are stored on the node and never shown." },
  { id: "dmr",          tag: "DM", label: "DMR",          sub: "Master & Slots",       crumb: "MODES / DMR",             title: "DMR Settings",          desc: "Color code and per-slot behaviour for Digital Mobile Radio." },
  { id: "modes",        tag: "MD", label: "Modes",        sub: "Digital Modes",        crumb: "MODES / DIGITAL",         title: "Digital Mode Control",  desc: "Which digital voice / data modes MMDVM-Host is handling." },
  { id: "gateways",     tag: "GW", label: "Gateways",     sub: "Cross-Mode Bridges",   crumb: "BRIDGES / GATEWAYS",      title: "Cross-Mode Gateways",   desc: "Transcoding bridges between digital voice modes." },
  { id: "network",      tag: "NW", label: "Network",      sub: "Wi-Fi & IP",           crumb: "SYSTEM / NETWORK",        title: "Network & Wi-Fi",       desc: "Wireless credentials and IP configuration for the host device." },
  { id: "expert",       tag: "SY", label: "Expert",       sub: "System & Config",      crumb: "SYSTEM / EXPERT",         title: "Expert & System",       desc: "Firmware versions and low-level configuration." },
];

const THEMES = [
  { key: "phosphor", color: "#35d07f", attr: "" },
  { key: "amber",    color: "#f0a935", attr: "amber" },
  { key: "ice",      color: "#4db8ff", attr: "ice" },
];

let state = { tab: "general", config: null, health: null };

const el = (t, cls, html) => { const e = document.createElement(t); if (cls) e.className = cls; if (html != null) e.innerHTML = html; return e; };
const esc = (s) => String(s == null ? "" : s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

// --- field builders ------------------------------------------------------
function card(title, rowsHTML) {
  return `<div class="card"><div class="card-head"><span class="sq"></span><span class="t">${esc(title)}</span></div>${rowsHTML}</div>`;
}
function textRow(label, value, opts = {}) {
  const cls = opts.accent ? "accent" : "";
  return `<div class="row"><label>${esc(label)}</label><input class="${cls}" value="${esc(value)}" readonly></div>`;
}
function unitRow(label, value, unit, opts = {}) {
  const cls = opts.accent ? "accent" : "";
  return `<div class="row"><label>${esc(label)}</label><div class="unit"><input class="${cls}" value="${esc(value)}" readonly><span class="u">${esc(unit)}</span></div></div>`;
}
function toggleRow(name, on) {
  return `<div class="toggle-row"><span class="name">${esc(name)}</span><span class="pill ${on ? "on" : "off"}">${on ? "ON" : "OFF"}</span></div>`;
}
function note(html) { return `<div class="note">${html}</div>`; }

const mhz = (hz) => (hz ? (Number(hz) / 1e6).toFixed(6) : "");

// --- per-tab panels ------------------------------------------------------
function panelGeneral(c) {
  const g = c.general || {};
  const left = card("STATION IDENTITY",
    textRow("Callsign", g.callsign) +
    textRow("DMR ID", g.dmr_id) +
    textRow("Location", g.location) +
    textRow("Dashboard URL", g.url));
  const radio = card("RADIO / FREQUENCY",
    unitRow("RX Frequency", mhz(g.rx_freq_hz), "MHz", { accent: true }) +
    unitRow("TX Frequency", mhz(g.tx_freq_hz), "MHz", { accent: true }) +
    textRow("Modem Port", g.modem_port) +
    unitRow("RF Power", g.power, "") +
    `<div class="row"><label>Duplex</label><span class="pill ${g.duplex ? "on" : "off"}">${g.duplex ? "DUPLEX" : "SIMPLEX"}</span></div>`);
  const cal = card("CALIBRATION",
    textRow("RX Offset", g.rx_offset) +
    textRow("TX Offset", g.tx_offset));
  return `<div class="grid2">${left}<div class="stack">${radio}${cal}</div></div>`;
}

function panelBrandmeister(c) {
  const nets = c.networks || [];
  if (!nets.length) return note("No DMR networks found in the DMRGateway config.");
  const cards = nets.map((n) => card(n.name,
    `<div class="row"><label>State</label><span class="pill ${n.enabled ? "on" : "off"}">${n.enabled ? "ENABLED" : "DISABLED"}</span></div>` +
    textRow("Server Address", n.address) +
    textRow("Port", n.port) +
    `<div class="row"><label>Password</label><input value="${n.has_password ? "••••••••••••" : ""}" readonly style="letter-spacing:2px;"></div>`)).join("");
  return `<div class="grid2">${cards}</div>`;
}

function panelDmr(c) {
  const d = c.dmr || {};
  const master = card("DMR MASTER",
    `<div class="row"><label>Enabled</label><span class="pill ${d.enable ? "on" : "off"}">${d.enable ? "ON" : "OFF"}</span></div>` +
    textRow("Color Code", d.color_code, { accent: true }) +
    textRow("DMR ID", d.id));
  const slots = card("TIME SLOTS & ADVANCED",
    toggleRow("Time Slot 1 Enabled", d.slot1) +
    toggleRow("Time Slot 2 Enabled", d.slot2) +
    toggleRow("Embedded LC Only", d.embedded_lc_only));
  return `<div class="grid2">${master}${slots}</div>`;
}

function panelModes(c) {
  const modes = c.modes || [];
  const cards = modes.map((m) => `
    <div class="mode-card ${m.enabled ? "on" : ""}">
      <div class="mode-top">
        <div><div class="mode-name">${esc(m.name)}</div><div class="mode-desc">${esc(m.key.toUpperCase())}</div></div>
        <div class="track"><div class="knob"></div></div>
      </div>
      <div class="mode-foot"><span class="d"></span><span class="s">${m.enabled ? "ENABLED" : "DISABLED"}</span></div>
    </div>`).join("");
  return `<div class="modes-grid">${cards}</div>`;
}

function panelExpert(c, h) {
  const rows = card("VERSIONS",
    textRow("Dashboard (waypointd)", (h && h.version) || "—") +
    textRow("MMDVM config", (c.sources && c.sources.mmdvm) || "—") +
    textRow("DMRGateway config", (c.sources && c.sources.dmrgateway) || "—"));
  return `<div class="grid2">${rows}${note("Raw INI editing, firmware, and power controls land with the configuration store. The store owns the schema so hand-editing keys (and its footguns) go away — <a href='https://github.com/KN4OQW/waypoint/issues/29'>waypoint#29</a>.")}</div>`;
}

function panelPending(what) {
  return note(`<b>${esc(what)}</b> settings aren't wired yet. This tab lands with the configuration store — a schema-versioned model of every setting, with the INI files as compiled outputs. Tracked in <a href="https://github.com/KN4OQW/waypoint/issues/1">waypoint#1</a> and <a href="https://github.com/KN4OQW/waypoint/issues/29">waypoint#29</a>.`);
}

function renderPanel() {
  const c = state.config || {};
  const box = document.getElementById("panels");
  switch (state.tab) {
    case "general":      box.innerHTML = panelGeneral(c); break;
    case "brandmeister": box.innerHTML = panelBrandmeister(c); break;
    case "dmr":          box.innerHTML = panelDmr(c); break;
    case "modes":        box.innerHTML = panelModes(c); break;
    case "expert":       box.innerHTML = panelExpert(c, state.health); break;
    case "gateways":     box.innerHTML = panelPending("Cross-mode gateway"); break;
    case "network":      box.innerHTML = panelPending("Network & Wi-Fi"); break;
    default:             box.innerHTML = "";
  }
}

// --- chrome --------------------------------------------------------------
function renderNav() {
  const nav = document.getElementById("nav");
  nav.querySelectorAll(".nav-item").forEach((n) => n.remove());
  TABS.forEach((t) => {
    const item = el("button", "nav-item" + (t.id === state.tab ? " on" : ""));
    item.innerHTML = `<div class="bar"></div><div class="tag">${esc(t.tag)}</div><div><div class="label">${esc(t.label)}</div><div class="sub">${esc(t.sub)}</div></div>`;
    item.onclick = () => selectTab(t.id);
    nav.appendChild(item);
  });
}

function selectTab(id) {
  state.tab = id;
  const t = TABS.find((x) => x.id === id) || TABS[0];
  document.getElementById("crumb").textContent = t.crumb;
  document.getElementById("title").textContent = t.title;
  document.getElementById("desc").textContent = t.desc;
  renderNav();
  renderPanel();
}

function renderThemes() {
  const box = document.getElementById("swatches");
  const cur = localStorage.getItem("wp-theme") || "phosphor";
  applyTheme(cur);
  THEMES.forEach((th) => {
    const s = el("button", "swatch" + (th.key === cur ? " on" : ""));
    s.title = th.key;
    s.innerHTML = `<span class="dot" style="background:${th.color}; box-shadow:0 0 7px ${th.color};"></span>`;
    s.onclick = () => { applyTheme(th.key); localStorage.setItem("wp-theme", th.key); renderThemes(); };
    box.appendChild(s);
  });
}
function applyTheme(key) {
  const th = THEMES.find((t) => t.key === key) || THEMES[0];
  if (th.attr) document.documentElement.setAttribute("data-theme", th.attr);
  else document.documentElement.removeAttribute("data-theme");
}

function renderStatus() {
  const h = state.health || {};
  document.getElementById("st-version").textContent = h.version || "—";
  document.getElementById("st-mode").textContent = h.demo ? "demo" : "live";
  document.getElementById("st-uptime").textContent = h.uptime || "—";
  document.getElementById("st-feed").textContent = h.demo ? "synthetic" : "MMDVM-Host";
  const c = state.config || {};
  document.getElementById("side-callsign").textContent = (c.general && c.general.callsign) || "—";
  const leds = document.getElementById("leds");
  leds.innerHTML = "";
  (c.modes || []).forEach((m) => {
    const d = el("div", "led-mode" + (m.enabled ? " on" : ""));
    d.title = m.name;
    d.innerHTML = `<span class="d"></span><span class="a">${esc(m.key.toUpperCase())}</span>`;
    leds.appendChild(d);
  });
  if (c.read_only) document.getElementById("ro-badge").classList.remove("hide");
}

async function load() {
  const [cfg, hlth] = await Promise.allSettled([
    fetch("/api/config").then((r) => r.json()),
    fetch("/api/health").then((r) => r.json()),
  ]);
  state.config = cfg.status === "fulfilled" ? cfg.value : {};
  state.health = hlth.status === "fulfilled" ? hlth.value : {};
  renderStatus();
  renderPanel();
}

renderNav();
renderThemes();
selectTab("general");
load();
