// Waypoint settings page. Reads the node's config from /api/config (served from
// the store) and writes edits back: PUT /api/config/{section} merges the changed
// fields into the store, then POST /api/config/apply regenerates the daemons'
// INIs and restarts them. Values are never hard-coded and never patched into
// INIs — the store is authoritative (RFC-0001).

const TABS = [
  { id: "general",      tag: "RF", label: "General",      sub: "Radio & Station",     crumb: "SYSTEM / GENERAL",        title: "General Configuration", desc: "Station identity, operating frequencies and modem hardware for this hotspot node." },
  { id: "brandmeister", tag: "BM", label: "BrandMeister", sub: "Network & Security",   crumb: "NETWORKS / BRANDMEISTER", title: "DMR Networks",          desc: "Master servers this node bridges DMR traffic to. Passwords are stored on the node and never shown." },
  { id: "dmr",          tag: "DM", label: "DMR",          sub: "Master & Slots",       crumb: "MODES / DMR",             title: "DMR Settings",          desc: "Color code and per-slot behaviour for Digital Mobile Radio." },
  { id: "ysf",          tag: "YS", label: "System Fusion", sub: "YSF / FCS reflectors", crumb: "MODES / SYSTEM FUSION",   title: "System Fusion (YSF)",   desc: "C4FM gateway: startup reflector or FCS room, Wires-X, and which reflector networks are on." },
  { id: "p25",          tag: "25", label: "P25",          sub: "NAC & Talkgroups",     crumb: "MODES / P25",             title: "P25 (Phase 1)",         desc: "APCO P25 gateway: network access code, startup talkgroups, and gateway behaviour." },
  { id: "nxdn",         tag: "NX", label: "NXDN",         sub: "RAN & Talkgroups",     crumb: "MODES / NXDN",            title: "NXDN",                  desc: "NXDN gateway: radio access number, startup talkgroups, and gateway behaviour." },
  { id: "modes",        tag: "MD", label: "Modes",        sub: "Digital Modes",        crumb: "MODES / DIGITAL",         title: "Digital Mode Control",  desc: "Which digital voice / data modes MMDVM-Host handles. Toggling one restarts the stack on Apply." },
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
let edit = {};              // section -> {field: value} working copy
let dirty = new Set();      // sections with unsaved changes
let applying = false;
let ysfRefs = [];           // cached YSF reflector list for the startup picker
let p25Refs = [];           // cached P25 talkgroup list for the startup-TG picker
let nxdnRefs = [];          // cached NXDN talkgroup list for the startup-TG picker

const el = (t, cls, html) => { const e = document.createElement(t); if (cls) e.className = cls; if (html != null) e.innerHTML = html; return e; };
const esc = (s) => String(s == null ? "" : s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const mhz = (hz) => (hz ? (Number(hz) / 1e6).toFixed(6) : "");

// --- edit state ----------------------------------------------------------
// Built from the redacted view; fields map to the store's typed sections. The
// General tab spans two sections (general + modem), so edits route accordingly.
function buildEdit(c) {
  const g = c.general || {}, d = c.dmr || {};
  edit = {
    general: { callsign: g.callsign, id: g.dmr_id, duplex: !!g.duplex, power: g.power, location: g.location, url: g.url },
    modem:   { rx_freq_hz: g.rx_freq_hz, tx_freq_hz: g.tx_freq_hz, port: g.modem_port, rx_offset: g.rx_offset, tx_offset: g.tx_offset },
    dmr:     { color_code: d.color_code, id: d.id, embedded_lc_only: !!d.embedded_lc_only },
    dmrnet:  { slot1: !!d.slot1, slot2: !!d.slot2 },
    modes:   Object.fromEntries((c.modes || []).map((m) => [m.key, !!m.enabled])),
    // password starts blank (blank = keep the stored one); has_password drives the placeholder.
    networks: (c.networks || []).map((n) => ({ name: n.name, address: n.address, port: n.port, enabled: !!n.enabled, password: "", rewrites: (n.rewrites || []).slice(), has_password: !!n.has_password })),
    ysfgw: ysfgwFrom(c.ysf || {}),
    p25: p25From(c.p25 || {}),
    p25gw: p25gwFrom(c.p25 || {}),
    nxdn: nxdnFrom(c.nxdn || {}),
    nxdngw: nxdngwFrom(c.nxdn || {}),
  };
  dirty = new Set();
  refreshActions();
}

function ysfgwFrom(y) {
  return {
    suffix: y.suffix || "RPT", startup: y.startup || "",
    wiresx_passthrough: !!y.wiresx_passthrough, wiresx_make_upper: !!y.wiresx_make_upper,
    reconnect: !!y.reconnect, revert: !!y.revert, inactivity_timeout: y.inactivity_timeout || "30",
    ysf_network: !!y.ysf_network, fcs_network: !!y.fcs_network, aprs: !!y.aprs,
  };
}

// The P25 view is flat (mode params + gateway settings); it splits back into two
// store sections: "p25" (MMDVM-Host [P25] params) and "p25gw" (P25Gateway.ini).
function p25From(p) {
  return {
    nac: p.nac || "293", self_only: !!p.self_only,
    override_uid_check: !!p.override_uid_check, remote_gateway: !!p.remote_gateway,
  };
}
function p25gwFrom(p) {
  return {
    static: p.static || "", voice: p.voice !== false,
    rf_hang_time: p.rf_hang_time || "120", net_hang_time: p.net_hang_time || "60",
  };
}

// The NXDN view is flat too; it splits back into "nxdn" (MMDVM-Host [NXDN]
// params) and "nxdngw" (NXDNGateway.ini). RAN is a decimal Radio Access Number,
// unlike P25's hex NAC.
function nxdnFrom(n) {
  return {
    ran: n.ran || "1", self_only: !!n.self_only, remote_gateway: !!n.remote_gateway,
  };
}
function nxdngwFrom(n) {
  return {
    static: n.static || "", voice: n.voice !== false,
    rf_hang_time: n.rf_hang_time || "120", net_hang_time: n.net_hang_time || "60",
  };
}

// cleanNet strips UI-only fields before sending to the store (which rejects
// unknown fields). A blank password means "keep the stored one".
function cleanNet(n) {
  return { name: n.name, address: n.address, port: n.port, enabled: !!n.enabled, password: n.password || "", rewrites: n.rewrites || [] };
}

function setField(sec, key, val) {
  if (!edit[sec]) edit[sec] = {};
  edit[sec][key] = val;
  dirty.add(sec);
  refreshActions();
}

// --- field builders (editable) -------------------------------------------
function card(title, rowsHTML) {
  return `<div class="card"><div class="card-head"><span class="sq"></span><span class="t">${esc(title)}</span></div>${rowsHTML}</div>`;
}
function row(label, inner) {
  return `<div class="row"><label>${esc(label)}</label>${inner}</div>`;
}
function input(sec, key, opts = {}) {
  const raw = (edit[sec] || {})[key];
  const disp = opts.kind === "mhz" ? mhz(raw) : (raw == null ? "" : raw);
  const cls = opts.accent ? "accent" : "";
  const inp = `<input class="${cls}" data-sec="${esc(sec)}" data-key="${esc(key)}" data-kind="${opts.kind || "str"}" value="${esc(disp)}">`;
  if (opts.unit) return row(opts.label, `<div class="unit">${inp}<span class="u">${esc(opts.unit)}</span></div>`);
  return row(opts.label, inp);
}
function toggle(sec, key, label, onTxt, offTxt) {
  const on = !!(edit[sec] || {})[key];
  const pill = `<span class="pill ${on ? "on" : "off"}" data-toggle="${esc(sec)}.${esc(key)}" style="cursor:pointer;">${on ? esc(onTxt || "ON") : esc(offTxt || "OFF")}</span>`;
  return row(label, pill);
}
function toggleRow(sec, key, name) {
  const on = !!(edit[sec] || {})[key];
  return `<div class="toggle-row"><span class="name">${esc(name)}</span><span class="pill ${on ? "on" : "off"}" data-toggle="${esc(sec)}.${esc(key)}" style="cursor:pointer;">${on ? "ON" : "OFF"}</span></div>`;
}
function note(html) { return `<div class="note">${html}</div>`; }

// --- panels --------------------------------------------------------------
function panelGeneral() {
  const left = card("STATION IDENTITY",
    input("general", "callsign", { label: "Callsign" }) +
    input("general", "id", { label: "DMR ID" }) +
    input("general", "location", { label: "Location" }) +
    input("general", "url", { label: "Dashboard URL" }));
  const radio = card("RADIO / FREQUENCY",
    input("modem", "rx_freq_hz", { label: "RX Frequency", kind: "mhz", unit: "MHz", accent: true }) +
    input("modem", "tx_freq_hz", { label: "TX Frequency", kind: "mhz", unit: "MHz", accent: true }) +
    input("modem", "port", { label: "Modem Port" }) +
    input("general", "power", { label: "RF Power", unit: "" }) +
    toggle("general", "duplex", "Duplex", "DUPLEX", "SIMPLEX"));
  const cal = card("CALIBRATION",
    input("modem", "rx_offset", { label: "RX Offset" }) +
    input("modem", "tx_offset", { label: "TX Offset" }));
  return `<div class="grid2">${left}<div class="stack">${radio}${cal}</div></div>`;
}

function panelDmr() {
  const master = card("DMR MASTER",
    toggle("modes", "dmr", "Enabled") +
    input("dmr", "color_code", { label: "Color Code", accent: true }) +
    input("dmr", "id", { label: "DMR ID" }));
  const slots = card("TIME SLOTS & ADVANCED",
    toggleRow("dmrnet", "slot1", "Time Slot 1 Enabled") +
    toggleRow("dmrnet", "slot2", "Time Slot 2 Enabled") +
    toggleRow("dmr", "embedded_lc_only", "Embedded LC Only"));
  return `<div class="grid2">${master}${slots}</div>`;
}

function panelModes() {
  const order = ["dstar", "dmr", "ysf", "p25", "nxdn", "m17", "pocsag", "fm"];
  const names = { dstar: "D-Star", dmr: "DMR", ysf: "System Fusion", p25: "P25", nxdn: "NXDN", m17: "M17", pocsag: "POCSAG", fm: "FM" };
  const cards = order.map((k) => {
    const on = !!(edit.modes || {})[k];
    return `
    <div class="mode-card ${on ? "on" : ""}" data-toggle="modes.${k}" style="cursor:pointer;">
      <div class="mode-top">
        <div><div class="mode-name">${esc(names[k])}</div><div class="mode-desc">${esc(k.toUpperCase())}</div></div>
        <div class="track"><div class="knob"></div></div>
      </div>
      <div class="mode-foot"><span class="d"></span><span class="s">${on ? "ENABLED" : "DISABLED"}</span></div>
    </div>`;
  }).join("");
  return `<div class="modes-grid">${cards}</div>`;
}

function panelBrandmeister() {
  const nets = edit.networks || [];
  const cards = nets.map((n, i) => `
    <div class="card">
      <div class="card-head">
        <span class="sq"></span>
        <input class="netname" data-net="${i}" data-nkey="name" value="${esc(n.name)}" placeholder="Network name">
        <span class="pill ${n.enabled ? "on" : "off"}" data-nettoggle="${i}" style="cursor:pointer;">${n.enabled ? "ENABLED" : "DISABLED"}</span>
        <button class="netdel" data-netdel="${i}" title="Remove network">✕</button>
      </div>
      ${row("Server Address", `<input data-net="${i}" data-nkey="address" value="${esc(n.address)}">`)}
      ${row("Port", `<input data-net="${i}" data-nkey="port" value="${esc(n.port)}">`)}
      ${row("Password", `<input data-net="${i}" data-nkey="password" type="password" value="${esc(n.password || "")}" placeholder="${n.has_password ? "•••••• unchanged" : "set password"}">`)}
      ${row("Rewrites", `<textarea class="rewrites" data-net="${i}" data-nkey="rewrites" rows="4" placeholder="TGRewrite0=2,9,2,9,1">${esc((n.rewrites || []).join("\n"))}</textarea>`)}
    </div>`).join("");
  const empty = nets.length ? "" : note("No DMR networks. Add one to bridge this hotspot to BrandMeister, TGIF, FreeDMR, or an HBLink server.");
  return `<div class="grid2">${cards}</div>${empty}<button class="btn ghost" id="net-add" style="margin-top:16px;">+ ADD NETWORK</button>`;
}

function panelExpert(c, h) {
  const rows = card("VERSIONS",
    `<div class="row"><label>Dashboard (waypointd)</label><input value="${esc((h && h.version) || "—")}" readonly></div>` +
    `<div class="row"><label>Config store</label><input value="${esc((c.sources && c.sources.store) || "—")}" readonly></div>`);
  return `<div class="grid2">${rows}${note("Raw INI editing and power controls land in a later slice. Config now lives in the store; the INIs are regenerated on Apply — <a href='https://github.com/KN4OQW/waypoint/issues/29'>waypoint#29</a>.")}</div>`;
}

function panelPending(what) {
  return note(`<b>${esc(what)}</b> settings aren't wired yet — a later slice of the configuration store (<a href="https://github.com/KN4OQW/waypoint/issues/1">waypoint#1</a>).`);
}

function panelYSF() {
  // Startup reflector picker: a datalist over the fetched hostlist so the user
  // can type-filter YSF reflectors / FCS rooms while still allowing a raw id.
  const opts = ysfRefs.map((r) => `<option value="${esc(r.name)}">${esc([r.country, r.description].filter(Boolean).join(" · "))}</option>`).join("");
  const startup = (edit.ysfgw || {}).startup || "";
  const gateway = card("GATEWAY",
    toggle("modes", "ysf", "System Fusion", "ENABLED", "DISABLED") +
    input("ysfgw", "suffix", { label: "Suffix (RPT/ND)" }) +
    row("Startup reflector", `<input data-sec="ysfgw" data-key="startup" list="ysf-refs" value="${esc(startup)}" placeholder="e.g. FCS00290 or a YSF reflector"><datalist id="ysf-refs">${opts}</datalist>`) +
    input("ysfgw", "inactivity_timeout", { label: "Inactivity revert", unit: "min" }));
  const behaviour = card("BEHAVIOUR",
    toggleRow("ysfgw", "wiresx_passthrough", "Wires-X passthrough (advanced — leave off for local control)") +
    toggleRow("ysfgw", "wiresx_make_upper", "Wires-X uppercase") +
    toggleRow("ysfgw", "revert", "Revert to startup on inactivity") +
    toggleRow("ysfgw", "reconnect", "Reconnect on link loss"));
  const networks = card("REFLECTOR NETWORKS",
    toggleRow("ysfgw", "ysf_network", "YSF reflector network") +
    toggleRow("ysfgw", "fcs_network", "FCS room network") +
    toggleRow("ysfgw", "aprs", "APRS position beacon"));
  const hint = ysfRefs.length ? "" : note("Reflector list not loaded yet (fetched from the YSF register on a schedule). You can still type a reflector id above.");
  return `<div class="grid2">${gateway}<div class="stack">${behaviour}${networks}</div></div>${hint}`;
}

function panelP25() {
  // Startup-TG picker: a datalist over the fetched talkgroup list. Static is a
  // comma-separated list, so the datalist is a reference the user types from.
  const opts = p25Refs.map((r) => `<option value="${esc(r.designator)}">${esc([r.name, r.country, r.sponsor].filter(Boolean).join(" · "))}</option>`).join("");
  const stat = (edit.p25gw || {}).static || "";
  const gateway = card("GATEWAY",
    toggle("modes", "p25", "P25", "ENABLED", "DISABLED") +
    input("p25", "nac", { label: "NAC (hex)", accent: true }) +
    row("Startup talkgroups", `<input data-sec="p25gw" data-key="static" list="p25-refs" value="${esc(stat)}" placeholder="comma-separated TGs, e.g. 10100,10200"><datalist id="p25-refs">${opts}</datalist>`) +
    toggleRow("p25gw", "voice", "Voice announcements"));
  const behaviour = card("BEHAVIOUR",
    toggleRow("p25", "self_only", "Self only (accept only my ID)") +
    toggleRow("p25", "override_uid_check", "Override UID check") +
    toggleRow("p25", "remote_gateway", "Remote gateway (advanced — leave off for local control)"));
  const timers = card("HANG TIMERS",
    input("p25gw", "rf_hang_time", { label: "RF hang", unit: "sec" }) +
    input("p25gw", "net_hang_time", { label: "Network hang", unit: "sec" }));
  const hint = p25Refs.length ? "" : note("Talkgroup list not loaded yet (fetched from the P25 register on a schedule). You can still type talkgroup numbers above.");
  return `<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}</div></div>${hint}`;
}

function panelNXDN() {
  // Startup-TG picker: a datalist over the fetched talkgroup list. Static is a
  // comma-separated list, so the datalist is a reference the user types from.
  const opts = nxdnRefs.map((r) => `<option value="${esc(r.designator)}">${esc([r.name, r.country, r.sponsor].filter(Boolean).join(" · "))}</option>`).join("");
  const stat = (edit.nxdngw || {}).static || "";
  const gateway = card("GATEWAY",
    toggle("modes", "nxdn", "NXDN", "ENABLED", "DISABLED") +
    input("nxdn", "ran", { label: "RAN", accent: true }) +
    row("Startup talkgroups", `<input data-sec="nxdngw" data-key="static" list="nxdn-refs" value="${esc(stat)}" placeholder="comma-separated TGs, e.g. 10200,65000"><datalist id="nxdn-refs">${opts}</datalist>`) +
    toggleRow("nxdngw", "voice", "Voice announcements"));
  const behaviour = card("BEHAVIOUR",
    toggleRow("nxdn", "self_only", "Self only (accept only my ID)") +
    toggleRow("nxdn", "remote_gateway", "Remote gateway (advanced — leave off for local control)"));
  const timers = card("HANG TIMERS",
    input("nxdngw", "rf_hang_time", { label: "RF hang", unit: "sec" }) +
    input("nxdngw", "net_hang_time", { label: "Network hang", unit: "sec" }));
  const hint = nxdnRefs.length ? "" : note("Talkgroup list not loaded yet (fetched from the NXDN register on a schedule). You can still type talkgroup numbers above.");
  return `<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}</div></div>${hint}`;
}

function renderPanel() {
  const c = state.config || {};
  const box = document.getElementById("panels");
  switch (state.tab) {
    case "general":      box.innerHTML = panelGeneral(); break;
    case "dmr":          box.innerHTML = panelDmr(); break;
    case "ysf":          box.innerHTML = panelYSF(); break;
    case "p25":          box.innerHTML = panelP25(); break;
    case "nxdn":         box.innerHTML = panelNXDN(); break;
    case "modes":        box.innerHTML = panelModes(); break;
    case "brandmeister": box.innerHTML = panelBrandmeister(); break;
    case "expert":       box.innerHTML = panelExpert(c, state.health); break;
    case "gateways":     box.innerHTML = panelPending("Cross-mode gateway"); break;
    case "network":      box.innerHTML = panelPending("Network & Wi-Fi"); break;
    default:             box.innerHTML = "";
  }
}

// --- apply / reset -------------------------------------------------------
function refreshActions() {
  const has = dirty.size > 0 && !applying;
  document.getElementById("btn-apply").disabled = !has;
  document.getElementById("btn-reset").disabled = !has;
  const badge = document.getElementById("ro-badge");
  badge.textContent = dirty.size ? dirty.size + " UNSAVED" : "";
  badge.classList.toggle("hide", dirty.size === 0);
  badge.style.color = "var(--warn)";
}

function banner(msg, kind) {
  let b = document.getElementById("save-banner");
  if (!b) {
    b = el("div");
    b.id = "save-banner";
    b.style.cssText = "margin:0 0 18px; padding:11px 14px; border-radius:8px; font-family:var(--mono); font-size:12px;";
    document.getElementById("panels").before(b);
  }
  b.textContent = msg;
  b.style.background = kind === "bad" ? "rgba(255,107,107,0.08)" : "var(--accent-soft)";
  b.style.color = kind === "bad" ? "var(--bad)" : "var(--accent)";
  b.style.border = "1px solid " + (kind === "bad" ? "rgba(255,107,107,0.4)" : "var(--accent)");
  b.hidden = false;
}

async function apply() {
  if (!dirty.size || applying) return;
  applying = true;
  const btn = document.getElementById("btn-apply");
  btn.textContent = "APPLYING…";
  refreshActions();
  try {
    for (const sec of dirty) {
      const payload = sec === "networks" ? edit.networks.map(cleanNet) : edit[sec];
      const r = await fetch("/api/config/" + sec, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      if (!r.ok) throw new Error(sec + ": " + (await r.text()).trim());
    }
    const r = await fetch("/api/config/apply", { method: "POST" });
    if (!r.ok) throw new Error("apply: " + (await r.text()).trim());
    const j = await r.json();
    applying = false;
    await load();
    banner("Applied — restarted " + ((j.restarted || []).join(", ") || "nothing"), "ok");
  } catch (err) {
    applying = false;
    banner(String(err.message || err), "bad");
    refreshActions();
  } finally {
    btn.textContent = "APPLY CHANGES";
  }
}

function reset() {
  banner("", "ok");
  document.getElementById("save-banner") && (document.getElementById("save-banner").hidden = true);
  buildEdit(state.config);
  renderPanel();
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
  if (!TABS.some((x) => x.id === id)) id = TABS[0].id;
  state.tab = id;
  const t = TABS.find((x) => x.id === id);
  if (location.hash !== "#" + id) history.replaceState(null, "", "#" + id);
  document.getElementById("crumb").textContent = t.crumb;
  document.getElementById("title").textContent = t.title;
  document.getElementById("desc").textContent = t.desc;
  renderNav();
  renderPanel();
}

function renderThemes() {
  const box = document.getElementById("swatches");
  box.innerHTML = ""; // re-render replaces the swatches instead of appending
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
  const h = state.health || {}, c = state.config || {};
  document.getElementById("st-version").textContent = h.version || "—";
  document.getElementById("st-mode").textContent = h.demo ? "demo" : "live";
  document.getElementById("st-uptime").textContent = h.uptime || "—";
  document.getElementById("st-feed").textContent = h.demo ? "synthetic" : "MMDVM-Host";
  document.getElementById("side-callsign").textContent = (c.general && c.general.callsign) || "—";
  const leds = document.getElementById("leds");
  leds.innerHTML = "";
  (c.modes || []).forEach((m) => {
    const d = el("div", "led-mode" + (m.enabled ? " on" : ""));
    d.title = m.name;
    d.innerHTML = `<span class="d"></span><span class="a">${esc(m.key.toUpperCase())}</span>`;
    leds.appendChild(d);
  });
}

async function load() {
  const [cfg, hlth] = await Promise.allSettled([
    fetch("/api/config").then((r) => r.json()),
    fetch("/api/health").then((r) => r.json()),
  ]);
  state.config = cfg.status === "fulfilled" ? cfg.value : {};
  state.health = hlth.status === "fulfilled" ? hlth.value : {};
  buildEdit(state.config);
  renderStatus();
  renderPanel();
  // Reflector lists load lazily; refresh the relevant panel if it's showing.
  try {
    ysfRefs = await fetch("/api/ysf/reflectors").then((r) => r.json());
    if (state.tab === "ysf") renderPanel();
  } catch { /* offline — the picker still accepts a typed id */ }
  try {
    p25Refs = await fetch("/api/p25/reflectors").then((r) => r.json());
    if (state.tab === "p25") renderPanel();
  } catch { /* offline — the picker still accepts a typed TG */ }
  try {
    nxdnRefs = await fetch("/api/nxdn/reflectors").then((r) => r.json());
    if (state.tab === "nxdn") renderPanel();
  } catch { /* offline — the picker still accepts a typed TG */ }
}

// text edits update the working copy; toggles flip a bool and re-render.
document.getElementById("panels").addEventListener("input", (e) => {
  const t = e.target;
  if (!t.dataset) return;
  if (t.dataset.sec) {
    let v = t.value;
    if (t.dataset.kind === "mhz") { const f = parseFloat(v); v = isNaN(f) ? "" : String(Math.round(f * 1e6)); }
    setField(t.dataset.sec, t.dataset.key, v);
    return;
  }
  if (t.dataset.net != null) {
    const i = +t.dataset.net, key = t.dataset.nkey;
    let v = t.value;
    if (key === "rewrites") v = v.split("\n").map((s) => s.trim()).filter(Boolean);
    edit.networks[i][key] = v;
    dirty.add("networks");
    refreshActions();
  }
});
document.getElementById("panels").addEventListener("click", (e) => {
  const tg = e.target.closest("[data-toggle]");
  if (tg) {
    const [sec, key] = tg.dataset.toggle.split(".");
    setField(sec, key, !(edit[sec] || {})[key]);
    renderPanel();
    return;
  }
  const nt = e.target.closest("[data-nettoggle]");
  if (nt) { const i = +nt.dataset.nettoggle; edit.networks[i].enabled = !edit.networks[i].enabled; dirty.add("networks"); renderPanel(); refreshActions(); return; }
  const nd = e.target.closest("[data-netdel]");
  if (nd) { edit.networks.splice(+nd.dataset.netdel, 1); dirty.add("networks"); renderPanel(); refreshActions(); return; }
  if (e.target.id === "net-add") {
    edit.networks.push({ name: "New Network", address: "", port: "62031", enabled: false, password: "", rewrites: [], has_password: false });
    dirty.add("networks"); renderPanel(); refreshActions();
  }
});
document.getElementById("btn-apply").onclick = apply;
document.getElementById("btn-reset").onclick = reset;

renderNav();
renderThemes();
selectTab((location.hash || "").slice(1) || "general");
load();
