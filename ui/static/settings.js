// Waypoint settings page. Reads the node's config from /api/config (served from
// the store) and writes edits back: PUT /api/config/{section} merges the changed
// fields into the store, then POST /api/config/apply regenerates the daemons'
// INIs and restarts them. Values are never hard-coded and never patched into
// INIs — the store is authoritative (RFC-0001).

const TABS = [
  { id: "general",      tag: "RF", label: "General",      sub: "Radio & Station",     crumb: "SYSTEM / GENERAL",        title: "General Configuration", desc: "Station identity, operating frequencies and modem hardware for this hotspot node." },
  { id: "setup",        tag: "SU", label: "Setup",         sub: "Control & Display",    crumb: "SYSTEM / SETUP",          title: "Control Software & Display", desc: "TRX mode and the MMDVM-Host display driver. Waypoint runs display-free (status is served over MQTT); these fields are here for parity and for nodes driving a physical panel." },
  { id: "brandmeister", tag: "BM", label: "BrandMeister", sub: "Network & Security",   crumb: "NETWORKS / BRANDMEISTER", title: "DMR Networks",          desc: "Master servers this node bridges DMR traffic to. Passwords are stored on the node and never shown." },
  { id: "dmr",          tag: "DM", label: "DMR",          sub: "Master & Slots",       crumb: "MODES / DMR",             title: "DMR Settings",          desc: "Color code and per-slot behaviour for Digital Mobile Radio." },
  { id: "dstar",        tag: "DS", label: "D-Star",        sub: "ircDDB & reflectors",  crumb: "MODES / D-STAR",          title: "D-Star",                desc: "D-Star gateway: module band letter, ircDDB callsign routing, startup reflector, and which reflector protocols are on." },
  { id: "ysf",          tag: "YS", label: "System Fusion", sub: "YSF / FCS reflectors", crumb: "MODES / SYSTEM FUSION",   title: "System Fusion (YSF)",   desc: "C4FM gateway: startup reflector or FCS room, Wires-X, and which reflector networks are on." },
  { id: "p25",          tag: "25", label: "P25",          sub: "NAC & Talkgroups",     crumb: "MODES / P25",             title: "P25 (Phase 1)",         desc: "APCO P25 gateway: network access code, startup talkgroups, and gateway behaviour." },
  { id: "nxdn",         tag: "NX", label: "NXDN",         sub: "RAN & Talkgroups",     crumb: "MODES / NXDN",            title: "NXDN",                  desc: "NXDN gateway: radio access number, startup talkgroups, and gateway behaviour." },
  { id: "m17",          tag: "17", label: "M17",          sub: "CAN & Reflectors",     crumb: "MODES / M17",             title: "M17",                   desc: "M17 gateway: channel access number, startup reflector + module, and gateway behaviour." },
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
let dstarRefs = [];         // cached D-Star reflector list for the startup picker
let m17Refs = [];           // cached M17 reflector list for the startup picker

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
    display: displayFrom(c.display || {}),
    dmr:     { color_code: d.color_code, id: d.id, embedded_lc_only: !!d.embedded_lc_only, dump_ta_data: !!d.dump_ta_data, beacons: !!d.beacons, self_only: !!d.self_only },
    dmrnet:  { slot1: !!d.slot1, slot2: !!d.slot2 },
    modes:   Object.fromEntries((c.modes || []).map((m) => [m.key, !!m.enabled])),
    // password starts blank (blank = keep the stored one); has_password drives the placeholder.
    networks: (c.networks || []).map((n) => ({ name: n.name, type: n.type || "custom", address: n.address, port: n.port, primary: !!n.primary, options: n.options || "", essid: n.essid || "", enabled: !!n.enabled, password: "", has_password: !!n.has_password, auto_rewrite: !!n.auto_rewrite, tg_list_file: n.tg_list_file || "", xlx_startup: n.xlx_startup || "", xlx_module: n.xlx_module || "", xlx_slot: n.xlx_slot || "2", rewrites: (n.rewrites || []).slice() })),
    routes: (c.routes || []).map((r) => ({ slot: r.slot || "2", tg: r.tg || "", network: r.network || "" })),
    ysfgw: ysfgwFrom(c.ysf || {}),
    p25: p25From(c.p25 || {}),
    p25gw: p25gwFrom(c.p25 || {}),
    nxdn: nxdnFrom(c.nxdn || {}),
    nxdngw: nxdngwFrom(c.nxdn || {}),
    dstar: dstarFrom(c.dstar || {}),
    dstargw: dstargwFrom(c.dstar || {}),
    m17: m17From(c.m17 || {}),
    m17gw: m17gwFrom(c.m17 || {}),
  };
  dirty = new Set();
  refreshActions();
}

// The D-Star view is flat (mode params + gateway settings); it splits back into
// two store sections: "dstar" (MMDVM-Host [D-Star] params) and "dstargw"
// (dstargateway.cfg). The Module band letter must match on both sides — it lives
// in the "dstar" section and the renderer mirrors it into the gateway Band.
function dstarFrom(d) {
  return {
    module: d.module || "B", self_only: !!d.self_only, remote_gateway: !!d.remote_gateway,
  };
}
// ircddb_password starts blank; blank means "keep the stored one" (the store
// merge preserves fields the payload omits — apply() drops the blank password).
// has_ircddb_password drives the placeholder.
function dstargwFrom(d) {
  return {
    reflector: d.reflector || "", reflector_reconnect: d.reflector_reconnect || "Never",
    ircddb_hostname: d.ircddb_hostname || "ircv4.openquad.net",
    ircddb_username: d.ircddb_username || "", ircddb_password: "",
    has_ircddb_password: !!d.has_ircddb_password,
    dextra: d.dextra !== false, dplus: d.dplus !== false, dplus_login: d.dplus_login || "",
    dcs: d.dcs !== false, xlx: d.xlx !== false,
  };
}

// The "display" section maps to MMDVM-Host's [Display] surface (the [General]
// Display selector + per-driver subsections). One store section, no secrets.
function displayFrom(d) {
  return {
    type: d.type || "None", oled_type: d.oled_type || "3", port: d.port || "modem",
    nextion_layout: d.nextion_layout || "0",
    hd44780_rows: d.hd44780_rows || "2", hd44780_cols: d.hd44780_cols || "16",
    hd44780_i2c_addr: d.hd44780_i2c_addr || "0x20",
  };
}

function ysfgwFrom(y) {
  return {
    suffix: y.suffix || "RPT", startup: y.startup || "",
    wiresx_passthrough: !!y.wiresx_passthrough,
    revert: !!y.revert, inactivity_timeout: y.inactivity_timeout || "30",
    ysf_network: !!y.ysf_network, fcs_network: !!y.fcs_network, aprs: !!y.aprs,
    enable_dgid: !!y.enable_dgid, ycs_network: !!y.ycs_network,
    upper_hostfiles: !!y.upper_hostfiles,
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
// The M17 view is flat too; it splits back into "m17" (MMDVM-Host [M17] params)
// and "m17gw" (M17Gateway.ini). CAN is a decimal Channel Access Number; M17 has
// no remote-gateway toggle but adds AllowEncryption.
function m17From(n) {
  return {
    can: n.can || "0", self_only: !!n.self_only, allow_encryption: !!n.allow_encryption,
  };
}
function m17gwFrom(n) {
  return {
    suffix: n.suffix || "H", startup: n.startup || "", revert: n.revert !== false,
    hang_time: n.hang_time || "240", voice: n.voice !== false,
  };
}

function nxdngwFrom(n) {
  return {
    static: n.static || "", voice: n.voice !== false,
    rf_hang_time: n.rf_hang_time || "120", net_hang_time: n.net_hang_time || "60",
  };
}

// cleanNet strips UI-only fields (has_password) before sending to the store,
// which rejects unknown fields. A blank password means "keep the stored one".
// Raw rewrites are sent only for a custom network; typed networks generate them.
function cleanNet(n) {
  return {
    name: n.name, type: n.type || "custom", address: n.address, port: n.port,
    primary: !!n.primary, options: n.options || "", essid: n.essid || "", enabled: !!n.enabled,
    password: n.password || "", auto_rewrite: !!n.auto_rewrite, tg_list_file: n.tg_list_file || "",
    xlx_startup: n.xlx_startup || "", xlx_module: n.xlx_module || "", xlx_slot: n.xlx_slot || "2",
    rewrites: n.type === "custom" && !n.auto_rewrite ? (n.rewrites || []) : [],
  };
}

// cleanDstargw strips the UI-only has_ircddb_password flag (the store rejects
// unknown fields) and omits ircddb_password when blank, so the merge keeps the
// stored secret. A supplied password replaces it.
function cleanDstargw(d) {
  const out = {
    reflector: d.reflector || "", reflector_reconnect: d.reflector_reconnect || "Never",
    ircddb_hostname: d.ircddb_hostname || "", ircddb_username: d.ircddb_username || "",
    dextra: !!d.dextra, dplus: !!d.dplus, dplus_login: d.dplus_login || "",
    dcs: !!d.dcs, xlx: !!d.xlx,
  };
  if (d.ircddb_password) out.ircddb_password = d.ircddb_password;
  return out;
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
// extLink renders an external dashboard/manager link — a pure UI affordance (no
// daemon config), matching WPSD's BrandMeister/TGIF/SystemX links.
function extLink(href, text) { return `<a class="ext" href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(text)} ↗</a>`; }
// nodeLockRow is WPSD's "Node Lock", moved into the DMR panel: PRIVATE = [DMR]
// SelfOnly on (TX locked to this node's own DMR ID), PUBLIC = off (other DMR IDs
// allowed). It is one control over one bit — WPSD's separate "Node Lock" and
// "allow other DMR IDs" fields are two framings of the same setting.
function nodeLockRow() {
  const on = !!(edit.dmr || {}).self_only;
  return `<div class="toggle-row"><span class="name">Node Lock (Private / Public)</span><span class="pill ${on ? "on" : "off"}" data-toggle="dmr.self_only" style="cursor:pointer;">${on ? "PRIVATE" : "PUBLIC"}</span></div>`;
}

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
    toggleRow("dmr", "embedded_lc_only", "Embedded LC Only") +
    nodeLockRow());
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

// --- Setup: Control Software + Display ------------------------------------
// WPSD's "Setup" surface above the mode panels. Control Software is MMDVMHost-
// only by design (no DStarRepeater selector), so its one live control is TRX
// Mode (Simplex/Duplex) → general.duplex. Display maps the [General] Display
// selector + the driver subsection it points at; the type dropdown combines
// OLED Type 3/6 into one entry (WPSD does the same), split back into
// display.type + display.oled_type on change.
function panelDisplay() {
  const g = edit.general || (edit.general = {}), d = edit.display || (edit.display = {});

  const trxSel = `<select data-trxmode>` +
    [["simplex", "Simplex Node"], ["duplex", "Duplex Repeater"]]
      .map(([v, l]) => `<option value="${v}"${(v === "duplex") === !!g.duplex ? " selected" : ""}>${l}</option>`).join("") + `</select>`;
  const control = card("CONTROL SOFTWARE",
    row("Radio Control Software", `<input value="MMDVMHost" readonly>`) +
    row("TRX Mode", trxSel));

  // Combined display-type value: OLED folds its Type into the option (OLED3/OLED6).
  const typeVal = d.type === "OLED" ? "OLED" + (d.oled_type || "3") : (d.type || "None");
  const typeOpts = [
    ["None", "None"], ["OLED3", "OLED Type 3 (0.96\")"], ["OLED6", "OLED Type 6 (1.3\")"],
    ["Nextion", "Nextion"], ["HD44780", "HD44780"], ["TFT Serial", "TFT Serial"], ["LCDproc", "LCDproc"],
  ].map(([v, l]) => `<option value="${esc(v)}"${v === typeVal ? " selected" : ""}>${esc(l)}</option>`).join("");

  // Port list: the fixed set WPSD offers, plus the current value if it's something
  // else (e.g. an imported /dev/ttyAMA0) so selecting it is never lost.
  const portList = ["None", "modem", "/dev/ttyACM0", "/dev/ttyUSB0", "/dev/ttyS2", "/dev/ttyNextionDriver"];
  const cur = d.port || "modem";
  if (!portList.includes(cur)) portList.splice(2, 0, cur);
  const portOpts = portList.map((p) => `<option value="${esc(p)}"${p === cur ? " selected" : ""}>${esc(p)}</option>`).join("");

  let displayRows =
    row("Display Type", `<select data-displaytype>${typeOpts}</select>`) +
    row("Port", `<select data-sec="display" data-key="port">${portOpts}</select>`);

  // Nextion layout — only when a Nextion is selected.
  if (d.type === "Nextion") {
    const lay = d.nextion_layout || "0";
    const layOpts = [["0", "G4KLX"], ["2", "ON7LDS L2"], ["3", "ON7LDS L3"], ["4", "ON7LDS L3 HS"]]
      .map(([v, l]) => `<option value="${v}"${v === lay ? " selected" : ""}>${l}</option>`).join("");
    displayRows += row("Nextion Layout", `<select data-sec="display" data-key="nextion_layout">${layOpts}</select>`);
  }

  // HD44780 geometry + I2C wiring — only when HD44780 is selected. This node wires
  // over I2C (a PCF8574 adapter), so the I2C address is the wiring field; there is
  // no separate I2C-bus key in MMDVM-Host's [HD44780] section.
  if (d.type === "HD44780") {
    displayRows +=
      input("display", "hd44780_rows", { label: "Rows" }) +
      input("display", "hd44780_cols", { label: "Columns" }) +
      input("display", "hd44780_i2c_addr", { label: "I2C Address", accent: true });
  }

  const display = card("DISPLAY", displayRows);
  const hint = note("This MMDVM-Host build is <b>display-free</b> — it renders status over MQTT and ignores these keys. They are carried for WPSD parity and for a clone running stock MMDVM-Host or driving a physical panel.");
  return `<div class="grid2">${control}${display}</div>${hint}`;
}

// --- DMR networks (WPSD-style: routing generated from network type) -------
// The operator never hand-writes DMRGateway rewrite lines. Each network has a
// type whose dial-prefix routing is generated on the node; exactly one network
// is the primary catch-all (no prefix — this is what makes the TG9990 Parrot
// echo). The only routing table is the optional "tie a talkgroup to a gateway"
// override. A "custom" network keeps a raw-rules escape hatch.
// Fixed per-network sections mirroring Pi-Star's "DMR Configuration" body: a DMR
// Master (primary) selector, then BrandMeister / DMR+ / Custom / SystemX / TGIF
// / XLX blocks, then General DMR Settings and the talkgroup-routing override.
// Each section binds to the single network of its type in edit.networks, created
// on demand when its master/enable is set. Routing itself is generated on the
// node from type + primary — no hand-written rewrites.
let dmrMasters = []; // cached /api/dmr/masters, for the master dropdowns

const slotSelect = (sel, attrs) =>
  `<select class="mini" ${attrs}><option value="1"${String(sel) === "1" ? " selected" : ""}>TS1</option><option value="2"${String(sel) !== "1" ? " selected" : ""}>TS2</option></select>`;

function netOf(type) { return (edit.networks || []).find((n) => n.type === type); }
// ensureNet returns the network of a type, creating a disabled one if absent.
function ensureNet(type) {
  let n = netOf(type);
  if (!n) {
    // TGIF has a single fixed master (no dropdown); default its address.
    const addr = type === "tgif" ? "tgif.network" : "";
    n = { name: type, type, address: addr, port: type === "xlx" ? "62030" : "62031", primary: false,
          options: "", essid: "", enabled: false, password: "", has_password: false,
          auto_rewrite: type === "custom", tg_list_file: "", xlx_startup: "", xlx_module: "", xlx_slot: "2", rewrites: [] };
    (edit.networks = edit.networks || []).push(n);
  }
  return n;
}

const enPill = (type, n) => `<span class="pill ${n && n.enabled ? "on" : "off"}" data-neten="${type}" style="cursor:pointer;">${n && n.enabled ? "ENABLED" : "DISABLED"}</span>`;
const netField = (type, key, n, ph, pw) =>
  `<input data-netf="${type}" data-nkey="${key}"${pw ? ' type="password"' : ""} value="${esc(n ? (n[key] || "") : "")}" placeholder="${esc(ph || "")}">`;

// masterSelect renders the DMR_Hosts.txt masters for a category; picking one
// fills the network's address/name/port on the node side.
function masterSelect(type, cat, n) {
  const list = dmrMasters.filter((m) => m.category === cat);
  const cur = (n && n.address) || "";
  const opts = ['<option value="">— select master —</option>']
    .concat(list.map((m) => `<option value="${esc(m.address)}"${m.address === cur ? " selected" : ""}>${esc(m.name)}</option>`))
    .join("");
  return `<select data-dmrmaster="${type}">${opts}</select>${list.length ? "" : " <small style='color:var(--dim)'>host list loading…</small>"}`;
}

// essidSelect: None / 01..99 extended-ID suffix, per Pi-Star.
function essidSelect(type, n) {
  const cur = (n && n.essid) || "";
  let opts = `<option value=""${cur === "" ? " selected" : ""}>None</option>`;
  for (let i = 1; i <= 99; i++) { const v = String(i).padStart(2, "0"); opts += `<option value="${v}"${v === cur ? " selected" : ""}>${v}</option>`; }
  return `<select data-netf="${type}" data-nkey="essid">${opts}</select>`;
}

function sectionHead(title, type, n) {
  return `<div class="card-head"><span class="sq"></span><span class="t">${title}</span>${enPill(type, n)}</div>`;
}

function panelBrandmeister() {
  const d = edit.dmr || (edit.dmr = {});
  const bm = netOf("brandmeister"), dp = netOf("dmrplus"), sx = netOf("systemx"), tg = netOf("tgif"), xl = netOf("xlx");
  const primaryType = ((edit.networks || []).find((n) => n.primary) || {}).type || "brandmeister";
  const masterSel = [["brandmeister", "Brandmeister"], ["dmrplus", "DMR+ / FreeDMR / HBlink Network"], ["systemx", "SystemX"], ["tgif", "TGIF"]]
    .map(([v, l]) => `<option value="${v}"${v === primaryType ? " selected" : ""}>${l}</option>`).join("");

  const master = `<section class="card">
      <div class="card-head"><span class="sq"></span><span class="t">DMR Master</span></div>
      ${row("DMR Master", `<select data-dmrprimary>${masterSel}</select>`)}
    </section>`;

  const bmSec = `<section class="card">
      ${sectionHead("BrandMeister Network Settings", "brandmeister", bm)}
      ${row("BrandMeister Master", masterSelect("brandmeister", "brandmeister", bm))}
      ${row("BM Hotspot Security", `<input data-netf="brandmeister" data-nkey="password" type="password" value="${esc(bm ? bm.password || "" : "")}" placeholder="${bm && bm.has_password ? "•••••• unchanged" : ""}">`)}
      ${row("BrandMeister Network ESSID", essidSelect("brandmeister", bm))}
      ${row("BrandMeister Manager", extLink("https://brandmeister.network/?page=hotspots", "Manage hotspot & static TGs"))}
      ${row("BrandMeister Dashboards", extLink("https://brandmeister.network/", "Open dashboard"))}
    </section>`;

  const dpSec = `<section class="card">
      ${sectionHead("DMR+ / FreeDMR / HBlink Network Settings", "dmrplus", dp)}
      ${row("DMR Master", masterSelect("dmrplus", "dmrplus", dp))}
      ${row("Network Options", netField("dmrplus", "options", dp, ""))}
      ${row("ESSID", essidSelect("dmrplus", dp))}
    </section>`;

  const sxSec = `<section class="card">
      ${sectionHead("SystemX Network Settings", "systemx", sx)}
      ${row("SystemX Master", masterSelect("systemx", "systemx", sx))}
      ${row("Network Options", netField("systemx", "options", sx, ""))}
      ${row("ESSID", essidSelect("systemx", sx))}
      ${note("Dial SystemX talkgroups with the <b>4</b> prefix (e.g. TG 3021 → <b>4</b>003021 on TS2); routing is generated on the node.")}
    </section>`;

  const tgSec = `<section class="card">
      ${sectionHead("TGIF Network Settings", "tgif", tg)}
      ${row("TGIF Security Key", `<input data-netf="tgif" data-nkey="password" type="password" value="${esc(tg ? tg.password || "" : "")}" placeholder="${tg && tg.has_password ? "•••••• unchanged" : ""}">`)}
      ${row("ESSID", essidSelect("tgif", tg))}
      ${row("TGIF Dashboards", extLink("https://tgif.network/", "Open dashboard"))}
      ${note("Dial TGIF talkgroups with the <b>5</b> prefix (e.g. TG 31665 → <b>5</b>031665 on TS2).")}
    </section>`;

  const xlSec = `<section class="card">
      ${sectionHead("XLX Network Settings", "xlx", xl)}
      ${row("XLX Startup TG", netField("xlx", "xlx_startup", xl, ""))}
      ${row("XLX Startup Module", netField("xlx", "xlx_module", xl, ""))}
      ${row("Time Slot", slotSelect(xl && xl.xlx_slot, `data-netf="xlx" data-nkey="xlx_slot"`))}
    </section>`;

  const cc = d.color_code || "1";
  let ccOpts = "";
  for (let i = 0; i <= 15; i++) ccOpts += `<option value="${i}"${String(i) === String(cc) ? " selected" : ""}>${i}</option>`;
  const general = `<section class="card">
      <div class="card-head"><span class="sq"></span><span class="t">General DMR Settings</span></div>
      <div class="toggle-row"><span class="name">DMR Roaming Beacon</span><span class="pill ${d.beacons ? "on" : "off"}" data-toggle="dmr.beacons" style="cursor:pointer;">${d.beacons ? "ON" : "OFF"}</span></div>
      ${row("DMR Color Code", `<select data-sec="dmr" data-key="color_code">${ccOpts}</select>`)}
      <div class="toggle-row"><span class="name">DMR EmbeddedLCOnly</span><span class="pill ${d.embedded_lc_only ? "on" : "off"}" data-toggle="dmr.embedded_lc_only" style="cursor:pointer;">${d.embedded_lc_only ? "ON" : "OFF"}</span></div>
      <div class="toggle-row"><span class="name">DMR DumpTAData</span><span class="pill ${d.dump_ta_data ? "on" : "off"}" data-toggle="dmr.dump_ta_data" style="cursor:pointer;">${d.dump_ta_data ? "ON" : "OFF"}</span></div>
      ${nodeLockRow()}
      ${note("<b>Private</b> locks TX to this node's own DMR ID; <b>Public</b> allows other DMR IDs through the hotspot.")}
    </section>`;

  return `<div class="stack">${master}${bmSec}${dpSec}${sxSec}${tgSec}${xlSec}${general}</div>${routingTable()}`;
}

// The talkgroup routing override table — "tie this dialed TG to this gateway".
function routingTable() {
  const nets = (edit.networks || []).filter((n) => n.enabled);
  const routes = edit.routes || [];
  const netOpts = (sel) => nets.map((n) => `<option value="${esc(n.name)}"${n.name === sel ? " selected" : ""}>${esc(n.name)} (${esc(n.type)})</option>`).join("");
  const rows = routes.map((r, j) => `
    <div class="route-row">
      ${slotSelect(r.slot, `data-rtslot="${j}"`)}
      <input class="mini" data-rttg="${j}" value="${esc(r.tg)}" placeholder="dialed TG">
      <span class="arr" aria-hidden="true">→</span>
      <select class="mini" data-rtnet="${j}">${netOpts(r.network)}</select>
      <button class="netdel" data-rtdel="${j}" title="Remove route">✕</button>
    </div>`).join("");
  const body = routes.length
    ? `<div class="route-head"><span>Slot</span><span>Dialed TG</span><span></span><span>Gateway</span><span></span></div>${rows}`
    : `<div class="route-empty">No overrides — every talkgroup follows its network's prefix, and anything unrouted goes to the primary.</div>`;
  return `
    <div class="card" style="margin-top:16px;">
      <div class="route-title">TALKGROUP ROUTING</div>
      ${body}
      <button class="btn ghost mini-btn" id="route-add"${nets.length ? "" : " disabled"}>+ ADD ROUTE</button>
    </div>`;
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
    toggleRow("ysfgw", "revert", "Revert to startup on inactivity"));
  const networks = card("REFLECTOR NETWORKS",
    toggleRow("ysfgw", "ysf_network", "YSF reflector network") +
    toggleRow("ysfgw", "fcs_network", "FCS room network") +
    toggleRow("ysfgw", "aprs", "APRS position beacon"));
  // DG-ID gateway: swaps YSFGateway for DGIdGateway (mutually exclusive daemons).
  // With it on, the startup reflector links via a DG-ID (YCS network) and the
  // radio's Wires-X gateway sits on DG-ID 0.
  const dgid = card("DG-ID GATEWAY",
    toggleRow("ysfgw", "enable_dgid", "Use DGIdGateway (DG-ID addressed) instead of YSFGateway") +
    toggleRow("ysfgw", "ycs_network", "Link the startup reflector as a DG-ID network (YCS)") +
    toggleRow("ysfgw", "upper_hostfiles", "UPPERCASE reflector names in the hostlist"));
  const hint = ysfRefs.length ? "" : note("Reflector list not loaded yet (fetched from the YSF register on a schedule). You can still type a reflector id above.");
  return `<div class="grid2">${gateway}<div class="stack">${behaviour}${networks}${dgid}</div></div>${hint}`;
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

function panelDStar() {
  // Startup reflector picker: a datalist over the fetched hostlist so the user
  // can type-filter reflectors (REF/XRF/DCS) while still allowing a raw value.
  // The gateway wants "name module", e.g. "REF001 C", so the datalist offers
  // names the user completes with a band letter.
  const opts = dstarRefs.map((r) => `<option value="${esc(r.name)} ">${esc(r.type)}</option>`).join("");
  const gw = edit.dstargw || {};
  const reflector = gw.reflector || "";
  const gateway = card("GATEWAY",
    toggle("modes", "dstar", "D-Star", "ENABLED", "DISABLED") +
    input("dstar", "module", { label: "Module (band letter)", accent: true }) +
    row("Startup reflector", `<input data-sec="dstargw" data-key="reflector" list="dstar-refs" value="${esc(reflector)}" placeholder="e.g. REF001 C — blank for none"><datalist id="dstar-refs">${opts}</datalist>`) +
    input("dstargw", "reflector_reconnect", { label: "Reflector reconnect (min / Never / Fixed)" }));
  const ircddb = card("ircDDB (CALLSIGN ROUTING)",
    input("dstargw", "ircddb_hostname", { label: "ircDDB host" }) +
    input("dstargw", "ircddb_username", { label: "Username (blank = callsign)" }) +
    row("Password", `<input data-sec="dstargw" data-key="ircddb_password" type="password" value="${esc(gw.ircddb_password || "")}" placeholder="${gw.has_ircddb_password ? "•••••• unchanged" : "blank = anonymous"}">`));
  const behaviour = card("RF BEHAVIOUR",
    toggleRow("dstar", "self_only", "Self only (accept only my callsign)") +
    toggleRow("dstar", "remote_gateway", "Remote gateway (advanced — leave off for local control)"));
  const protocols = card("REFLECTOR PROTOCOLS",
    toggleRow("dstargw", "dextra", "DExtra (XRF)") +
    toggleRow("dstargw", "dplus", "D-Plus (REF — needs a registered callsign)") +
    row("D-Plus login", `<input data-sec="dstargw" data-key="dplus_login" value="${esc(gw.dplus_login || "")}" placeholder="registered callsign (blank = station callsign)">`) +
    toggleRow("dstargw", "dcs", "DCS") +
    toggleRow("dstargw", "xlx", "XLX"));
  const hint = dstarRefs.length ? "" : note("Reflector list not loaded yet (fetched from the pinned D-Star register on a schedule). You can still type a reflector above.");
  return `<div class="grid2"><div class="stack">${gateway}${ircddb}</div><div class="stack">${behaviour}${protocols}</div></div>${hint}`;
}

function panelM17() {
  const opts = m17Refs.map((r) => `<option value="${esc(r.name)} ">${esc(r.address)}</option>`).join("");
  const gw = edit.m17gw || {};
  const suffix = (gw.suffix || "H").toUpperCase();
  const suffixSel = `<select data-sec="m17gw" data-key="suffix">` +
    ["H", "R"].map((v) => `<option value="${v}"${v === suffix ? " selected" : ""}>${v === "H" ? "H — hotspot" : "R — repeater"}</option>`).join("") + `</select>`;
  const gateway = card("GATEWAY",
    toggle("modes", "m17", "M17", "ENABLED", "DISABLED") +
    input("m17", "can", { label: "CAN", accent: true }) +
    row("Startup reflector", `<input data-sec="m17gw" data-key="startup" list="m17-refs" value="${esc(gw.startup || "")}" placeholder="e.g. M17-M17 C — blank for none"><datalist id="m17-refs">${opts}</datalist>`) +
    row("Node suffix", suffixSel) +
    toggleRow("m17gw", "voice", "Voice announcements"));
  const behaviour = card("BEHAVIOUR",
    toggleRow("m17", "self_only", "Self only (accept only my callsign)") +
    toggleRow("m17", "allow_encryption", "Allow encrypted M17 frames") +
    toggleRow("m17gw", "revert", "Revert to startup reflector after inactivity"));
  const timers = card("HANG TIMER",
    input("m17gw", "hang_time", { label: "Network hang", unit: "sec" }));
  const hint = m17Refs.length ? "" : note("Reflector list not loaded yet (fetched from the M17 register on a schedule). You can still type a reflector above.");
  return `<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}</div></div>${hint}`;
}

function renderPanel() {
  const c = state.config || {};
  const box = document.getElementById("panels");
  switch (state.tab) {
    case "general":      box.innerHTML = panelGeneral(); break;
    case "setup":        box.innerHTML = panelDisplay(); break;
    case "dmr":          box.innerHTML = panelDmr(); break;
    case "dstar":        box.innerHTML = panelDStar(); break;
    case "ysf":          box.innerHTML = panelYSF(); break;
    case "p25":          box.innerHTML = panelP25(); break;
    case "nxdn":         box.innerHTML = panelNXDN(); break;
    case "m17":          box.innerHTML = panelM17(); break;
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
      const payload = sec === "networks" ? edit.networks.map(cleanNet)
        : sec === "routes" ? (edit.routes || []).filter((r) => r.tg && r.network)
        : sec === "dstargw" ? cleanDstargw(edit.dstargw)
        : edit[sec];
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
  try {
    dstarRefs = await fetch("/api/dstar/reflectors").then((r) => r.json());
    if (state.tab === "dstar") renderPanel();
  } catch { /* offline — the picker still accepts a typed reflector */ }
  try {
    m17Refs = await fetch("/api/m17/reflectors").then((r) => r.json());
    if (state.tab === "m17") renderPanel();
  } catch { /* offline — the picker still accepts a typed reflector */ }
  try {
    dmrMasters = await fetch("/api/dmr/masters").then((r) => r.json()) || [];
    if (state.tab === "brandmeister") renderPanel();
  } catch { /* offline — the master dropdowns show what's cached (may be empty) */ }
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
  // TRX Mode selector (Setup) — one control over general.duplex.
  if (t.dataset.trxmode != null) { setField("general", "duplex", t.value === "duplex"); return; }
  // Display Type selector (Setup) — combined value splits into type + oled_type;
  // re-render so the driver sub-fields (Nextion layout / HD44780) show or hide.
  if (t.dataset.displaytype != null) {
    const v = t.value;
    if (v === "OLED3") { setField("display", "type", "OLED"); setField("display", "oled_type", "3"); }
    else if (v === "OLED6") { setField("display", "type", "OLED"); setField("display", "oled_type", "6"); }
    else setField("display", "type", v);
    renderPanel();
    return;
  }
  // DMR Master (primary) selector — the primary is the no-prefix catch-all.
  if (t.dataset.dmrprimary != null) {
    const type = t.value, n = ensureNet(type);
    n.primary = true; n.enabled = true;
    (edit.networks || []).forEach((x) => { x.primary = x.type === type; });
    dirty.add("networks"); renderPanel(); refreshActions();
    return;
  }
  // Master dropdown: apply the chosen DMR_Hosts.txt master to the network.
  if (t.dataset.dmrmaster != null) {
    const type = t.dataset.dmrmaster, m = dmrMasters.find((x) => x.address === t.value), n = ensureNet(type);
    if (m) { n.address = m.address; n.port = m.port || n.port; if (!n.name || n.name === type) n.name = m.name; }
    else { n.address = t.value; }
    dirty.add("networks"); renderPanel(); refreshActions();
    return;
  }
  // talkgroup routing table: slot / dialed TG / target gateway.
  if (t.dataset.rtslot != null) { edit.routes[+t.dataset.rtslot].slot = t.value; dirty.add("routes"); refreshActions(); return; }
  if (t.dataset.rttg != null) { edit.routes[+t.dataset.rttg].tg = t.value; dirty.add("routes"); refreshActions(); return; }
  if (t.dataset.rtnet != null) { edit.routes[+t.dataset.rtnet].network = t.value; dirty.add("routes"); refreshActions(); return; }
  // per-network field, bound by network type (created on demand).
  if (t.dataset.netf != null) {
    ensureNet(t.dataset.netf)[t.dataset.nkey] = t.value;
    dirty.add("networks"); refreshActions();
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
  // per-network Enable toggle, bound by type (creates the network on demand).
  const en = e.target.closest("[data-neten]");
  if (en) { const n = ensureNet(en.dataset.neten); n.enabled = !n.enabled; if (!n.enabled) n.primary = false; dirty.add("networks"); renderPanel(); refreshActions(); return; }
  // per-network boolean toggle (e.g. custom Automatic Rewrite Rules).
  const nb = e.target.closest("[data-netbool]");
  if (nb) { const n = ensureNet(nb.dataset.netbool); n[nb.dataset.nbkey] = !n[nb.dataset.nbkey]; dirty.add("networks"); renderPanel(); refreshActions(); return; }
  const rtd = e.target.closest("[data-rtdel]");
  if (rtd) { edit.routes.splice(+rtd.dataset.rtdel, 1); dirty.add("routes"); renderPanel(); refreshActions(); return; }
  if (e.target.id === "route-add") {
    const firstEnabled = (edit.networks || []).find((n) => n.enabled) || {};
    (edit.routes = edit.routes || []).push({ slot: "2", tg: "", network: firstEnabled.name || "" });
    dirty.add("routes"); renderPanel(); refreshActions();
  }
});
document.getElementById("btn-apply").onclick = apply;
document.getElementById("btn-reset").onclick = reset;

renderNav();
renderThemes();
selectTab((location.hash || "").slice(1) || "general");
load();
