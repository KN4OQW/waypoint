// Waypoint settings page. Reads the node's config from /api/config (served from
// the store) and writes edits back: PUT /api/config/{section} merges the changed
// fields into the store, then POST /api/config/apply regenerates the daemons'
// INIs and restarts them. Values are never hard-coded and never patched into
// INIs — the store is authoritative (RFC-0001).

const TABS = [
  { id: "general",      tag: "RF", label: "General",      sub: "Radio & Station",     crumb: "SYSTEM / GENERAL",        title: "General Configuration", desc: "Station identity, operating frequencies and modem hardware for this hotspot node." },
  { id: "setup",        tag: "SU", label: "Setup",         sub: "Control & Display",    crumb: "SYSTEM / SETUP",          title: "Control Software & Display", desc: "TRX mode and the MMDVM-Host display driver. Waypoint runs display-free (status is served over MQTT); these fields are here for parity and for nodes driving a physical panel." },
  { id: "lcd",          tag: "LC", label: "LCD",           sub: "HD44780 Panel",        crumb: "SYSTEM / LCD",            title: "LCD Display",           desc: "Drive a physical HD44780 character panel over I2C, with pages of live status that rotate. Disabled by default; the node stays headless until you turn it on." },
  { id: "brandmeister", tag: "BM", label: "BrandMeister", sub: "Network & Security",   crumb: "NETWORKS / BRANDMEISTER", title: "DMR Networks",          desc: "Master servers this node bridges DMR traffic to. Passwords are stored on the node and never shown." },
  { id: "dmr",          tag: "DM", label: "DMR",          sub: "Master & Slots",       crumb: "MODES / DMR",             title: "DMR Settings",          desc: "Color code and per-slot behaviour for Digital Mobile Radio." },
  { id: "dstar",        tag: "DS", label: "D-Star",        sub: "ircDDB & reflectors",  crumb: "MODES / D-STAR",          title: "D-Star",                desc: "D-Star gateway: module band letter, ircDDB callsign routing, startup reflector, and which reflector protocols are on." },
  { id: "ysf",          tag: "YS", label: "System Fusion", sub: "YSF / FCS reflectors", crumb: "MODES / SYSTEM FUSION",   title: "System Fusion (YSF)",   desc: "C4FM gateway: startup reflector or FCS room, Wires-X, and which reflector networks are on." },
  { id: "p25",          tag: "25", label: "P25",          sub: "NAC & Talkgroups",     crumb: "MODES / P25",             title: "P25 (Phase 1)",         desc: "APCO P25 gateway: network access code, startup talkgroups, and gateway behaviour." },
  { id: "nxdn",         tag: "NX", label: "NXDN",         sub: "RAN & Talkgroups",     crumb: "MODES / NXDN",            title: "NXDN",                  desc: "NXDN gateway: radio access number, startup talkgroups, and gateway behaviour." },
  { id: "m17",          tag: "17", label: "M17",          sub: "CAN & Reflectors",     crumb: "MODES / M17",             title: "M17",                   desc: "M17 gateway: channel access number, startup reflector + module, and gateway behaviour." },
  { id: "pocsag",       tag: "PG", label: "POCSAG",       sub: "DAPNET Paging",        crumb: "MODES / POCSAG",          title: "POCSAG (DAPNET)",       desc: "Amateur paging: the paging channel and the DAPNETGateway login (server, callsign, AuthKey). The AuthKey is stored on the node and never shown." },
  { id: "fm",           tag: "FM", label: "FM",           sub: "Analog Voice",         crumb: "MODES / FM",              title: "FM (Analog)",           desc: "Analog FM has no gateway — just the MMDVM-Host [FM] parameters: CTCSS tone, timeout, kerchunk time, audio levels, and access mode." },
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
  const g = c.general || {}, d = c.dmr || {}, cm = c.crossmode || {};
  edit = {
    general: { callsign: g.callsign, id: g.dmr_id, duplex: !!g.duplex, power: g.power, location: g.location, url: g.url },
    modem:   { rx_freq_hz: g.rx_freq_hz, tx_freq_hz: g.tx_freq_hz, port: g.modem_port, rx_offset: g.rx_offset, tx_offset: g.tx_offset },
    display: displayFrom(c.display || {}),
    lcd: lcdFrom(c.lcd || {}),
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
    pocsag: pocsagFrom(c.pocsag || {}),
    fm: fmFrom(c.fm || {}),
    ysf2dmr: ysf2dmrFrom(cm.ysf2dmr || {}),
    dmr2ysf: dmr2ysfFrom(cm.dmr2ysf || {}),
    ysf2nxdn: ysf2nxdnFrom(cm.ysf2nxdn || {}),
    dmr2nxdn: dmr2nxdnFrom(cm.dmr2nxdn || {}),
    nxdn2dmr: nxdn2dmrFrom(cm.nxdn2dmr || {}),
  };
  dirty = new Set();
  refreshActions();
}

// --- cross-mode bridges --------------------------------------------------
// Each bridge is its own store section. The two DMR-master bridges (ysf2dmr,
// nxdn2dmr) carry a redacted password: it starts blank (blank = keep the stored
// one) and has_password drives the placeholder, exactly like the ircDDB password.
function ysf2dmrFrom(b) {
  return {
    enable: !!b.enable, dmr_id: b.dmr_id || "", master: b.master || "",
    options: b.options || "", tg: b.tg || "", password: "", has_password: !!b.has_password,
  };
}
function dmr2ysfFrom(b) {
  return { enable: !!b.enable, dmr_id: b.dmr_id || "", default_tg: b.default_tg || "" };
}
function ysf2nxdnFrom(b) {
  return { enable: !!b.enable, nxdn_id: b.nxdn_id || "", tg: b.tg || "" };
}
function dmr2nxdnFrom(b) {
  return { enable: !!b.enable, dmr_id: b.dmr_id || "", nxdn_id: b.nxdn_id || "" };
}
function nxdn2dmrFrom(b) {
  return {
    enable: !!b.enable, dmr_id: b.dmr_id || "", master: b.master || "",
    options: b.options || "", tg: b.tg || "", nxdn_tg: b.nxdn_tg || "",
    password: "", has_password: !!b.has_password,
  };
}

// cleanBridge strips the UI-only has_password flag (the store rejects unknown
// fields) and omits password when blank, so the merge keeps the stored secret.
// A supplied password replaces it. No-op for a bridge that has no password field.
function cleanBridge(b) {
  const out = {};
  for (const k in b) {
    if (k === "has_password") continue;
    if (k === "password" && !b[k]) continue; // blank = keep stored
    out[k] = b[k];
  }
  return out;
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

// The "lcd" section drives the native HD44780 renderer (pages of live status).
// One store section, no secrets. Pages are copied so edits don't touch state.
function lcdFrom(l) {
  return {
    enabled: !!l.enabled,
    i2c_bus: l.i2c_bus || "/dev/i2c-1",
    i2c_address: l.i2c_address || "0x27",
    rows: l.rows || "4",
    cols: l.cols || "20",
    scroll_speed: l.scroll_speed || "300",
    activity_interrupt: l.activity_interrupt !== false,
    pages: (l.pages || []).map((p) => ({
      enabled: p.enabled !== false,
      name: p.name || "",
      duration: p.duration || "8",
      lines: (p.lines || []).slice(),
    })),
  };
}

// LCD_TOKENS mirrors the renderer's grounded token set (internal/lcd/tokens.go):
// the palette offers these, and lines are validated against them client-side.
const LCD_TOKENS = ["callsign", "dmr_id", "ip", "time", "date", "uptime", "version", "mode", "modes", "status", "lh_call", "lh_tg", "lh_mode", "lh_ber", "lh_rssi", "lh_ago"];
// unknownTokens returns the {tokens} in a line that aren't in LCD_TOKENS.
function unknownTokens(line) {
  const bad = [];
  const re = /\{([a-z0-9_]+)\}/g;
  let m;
  while ((m = re.exec(String(line || ""))) !== null) {
    if (!LCD_TOKENS.includes(m[1]) && !bad.includes(m[1])) bad.push(m[1]);
  }
  return bad;
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

// The POCSAG view is flat (mode enable + paging + DAPNET login); the enable is the
// "modes" section, everything else is the "pocsag" store section. auth_key starts
// blank (blank = keep the stored one); has_auth_key drives the placeholder, like
// the ircDDB password.
function pocsagFrom(p) {
  return {
    frequency: p.frequency || "439987500", server: p.server || "dapnet.afu.rwth-aachen.de",
    callsign: p.callsign || "", auth_key: "", has_auth_key: !!p.has_auth_key,
    whitelist: p.whitelist || "", blacklist: p.blacklist || "",
  };
}
// cleanPocsag strips the UI-only has_auth_key flag (the store rejects unknown
// fields) and omits auth_key when blank, so the merge keeps the stored secret.
// A supplied AuthKey replaces it.
function cleanPocsag(p) {
  const out = {
    frequency: p.frequency || "", server: p.server || "", callsign: p.callsign || "",
    whitelist: p.whitelist || "", blacklist: p.blacklist || "",
  };
  if (p.auth_key) out.auth_key = p.auth_key;
  return out;
}

// The FM view is flat too; the enable is the "modes" section, the analog params
// are the "fm" store section. No gateway, no secrets.
function fmFrom(f) {
  return {
    ctcss: f.ctcss || "88.4", timeout: f.timeout || "180", kerchunk_time: f.kerchunk_time || "0",
    rf_audio_boost: f.rf_audio_boost || "1", ext_audio_boost: f.ext_audio_boost || "1",
    access_mode: f.access_mode || "1",
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
// Toggles render as real <button>s (keyboard-operable, Enter/Space) with
// aria-pressed exposing on/off state to screen readers — so status is never
// carried by the accent colour alone. The descriptive label is the button's
// accessible name; aria-pressed carries the state.
function toggle(sec, key, label, onTxt, offTxt) {
  const on = !!(edit[sec] || {})[key];
  const pill = `<button type="button" class="pill ${on ? "on" : "off"}" data-toggle="${esc(sec)}.${esc(key)}" aria-pressed="${on}" aria-label="${esc(label)}">${on ? esc(onTxt || "ON") : esc(offTxt || "OFF")}</button>`;
  return row(label, pill);
}
function toggleRow(sec, key, name) {
  const on = !!(edit[sec] || {})[key];
  return `<div class="toggle-row"><span class="name">${esc(name)}</span><button type="button" class="pill ${on ? "on" : "off"}" data-toggle="${esc(sec)}.${esc(key)}" aria-pressed="${on}" aria-label="${esc(name)}">${on ? "ON" : "OFF"}</button></div>`;
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
  return `<div class="toggle-row"><span class="name">Node Lock (Private / Public)</span><button type="button" class="pill ${on ? "on" : "off"}" data-toggle="dmr.self_only" aria-pressed="${on}" aria-label="Node Lock (Private / Public)">${on ? "PRIVATE" : "PUBLIC"}</button></div>`;
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
    // A whole mode tile is one big toggle: a real <button> so it's reachable by
    // Tab and flips on Enter/Space. aria-pressed carries the enabled state; the
    // "ENABLED/DISABLED" text and the LED both back up the accent colour.
    return `
    <button type="button" class="mode-card ${on ? "on" : ""}" data-toggle="modes.${k}" aria-pressed="${on}" aria-label="${esc(names[k])} mode">
      <div class="mode-top">
        <div><div class="mode-name">${esc(names[k])}</div><div class="mode-desc">${esc(k.toUpperCase())}</div></div>
        <div class="track" aria-hidden="true"><div class="knob"></div></div>
      </div>
      <div class="mode-foot"><span class="d" aria-hidden="true"></span><span class="s">${on ? "ENABLED" : "DISABLED"}</span></div>
    </button>`;
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

// --- LCD: native HD44780 page builder ------------------------------------
// A PANEL card for the wiring/geometry, then one card per rotating page. Each
// page has a name, an enable, a hold duration, and one line input per row; a
// token palette inserts {tokens} at the caret, and lines are validated so an
// unknown token is flagged (not silently blank). Saves go through the generic
// PUT /api/config/lcd. All controls are real buttons/inputs so the tab is
// keyboard-operable.
let lcdActive = null; // {page, row} of the last-focused line input, for token insertion

// lcdToggleRow renders an accessible pill toggle (a real <button>) bound to an
// edit.lcd boolean, so Enter/Space work and state is exposed via aria-pressed.
function lcdToggleRow(key, name, onTxt, offTxt) {
  const on = !!(edit.lcd || {})[key];
  return `<div class="toggle-row"><span class="name">${esc(name)}</span><button type="button" class="pill ${on ? "on" : "off"}" data-toggle="lcd.${esc(key)}" aria-pressed="${on ? "true" : "false"}">${on ? esc(onTxt) : esc(offTxt)}</button></div>`;
}

function lcdSelect(key, opts, cur, extra) {
  const o = opts.map(([v, l]) => `<option value="${esc(v)}"${v === cur ? " selected" : ""}>${esc(l)}</option>`).join("");
  return `<select data-lcd-dim="${esc(key)}"${extra || ""}>${o}</select>`;
}

function pageCard(p, i, rows) {
  let lines = "";
  const bad = [];
  for (let j = 0; j < rows; j++) {
    const v = p.lines[j] || "";
    unknownTokens(v).forEach((u) => { if (!bad.includes(u)) bad.push(u); });
    lines += `<div class="lcd-line"><label class="lcd-linelabel" for="lcd-l-${i}-${j}">Row ${j + 1}</label>` +
      `<input id="lcd-l-${i}-${j}" class="lcd-lineinput" data-lcdline="${i}" data-lcdrow="${j}" value="${esc(v)}" placeholder="text and {tokens}" aria-label="Page ${i + 1} row ${j + 1}"></div>`;
  }
  const warn = `<div class="lcd-warn${bad.length ? "" : " hide"}" role="alert" data-lcdwarn="${i}">${warnText(bad)}</div>`;
  const palette = `<div class="lcd-tokens" role="group" aria-label="Insert a token into page ${i + 1}">` +
    LCD_TOKENS.map((tk) => `<button type="button" class="lcd-tok" data-lcdtoken="${esc(tk)}" data-lcdpageidx="${i}" title="Insert {${esc(tk)}}">{${esc(tk)}}</button>`).join("") + `</div>`;
  return `<section class="card lcd-page">
      <div class="card-head lcd-pagehead">
        <span class="sq" aria-hidden="true"></span>
        <input class="lcd-pagename" data-lcdpage="${i}" data-lcdkey="name" value="${esc(p.name || "")}" placeholder="Page name" aria-label="Page ${i + 1} name">
        <button type="button" class="pill ${p.enabled ? "on" : "off"}" data-lcdpageen="${i}" aria-pressed="${p.enabled ? "true" : "false"}">${p.enabled ? "ENABLED" : "DISABLED"}</button>
        <span class="lcd-dur"><input class="mini" data-lcdpage="${i}" data-lcdkey="duration" value="${esc(p.duration || "")}" inputmode="numeric" aria-label="Page ${i + 1} hold seconds"> s</span>
        <button type="button" class="netdel" data-lcdpagedel="${i}" aria-label="Remove page ${i + 1}">✕</button>
      </div>
      ${lines}${warn}${palette}
    </section>`;
}

function warnText(bad) {
  if (!bad.length) return "";
  return `⚠ Unknown token${bad.length > 1 ? "s" : ""}: ${bad.map((u) => esc("{" + u + "}")).join(", ")} — check spelling; unknown tokens render blank.`;
}

// updatePageWarning refreshes one page's unknown-token notice without a full
// re-render, so typing in a line input never steals focus.
function updatePageWarning(i) {
  const el = document.querySelector(`[data-lcdwarn="${i}"]`);
  if (!el) return;
  const bad = [];
  (edit.lcd.pages[i].lines || []).forEach((ln) => unknownTokens(ln).forEach((u) => { if (!bad.includes(u)) bad.push(u); }));
  el.innerHTML = warnText(bad);
  el.classList.toggle("hide", bad.length === 0);
}

function panelLCD() {
  const l = edit.lcd || (edit.lcd = lcdFrom({}));
  const rows = Math.max(1, parseInt(l.rows, 10) || 4);
  const panel = card("PANEL",
    lcdToggleRow("enabled", "Driver enabled", "ENABLED", "DISABLED") +
    input("lcd", "i2c_bus", { label: "I2C bus" }) +
    input("lcd", "i2c_address", { label: "I2C address", accent: true }) +
    row("Rows", lcdSelect("rows", [["2", "2 rows"], ["4", "4 rows"]], l.rows)) +
    row("Columns", lcdSelect("cols", [["16", "16 columns"], ["20", "20 columns"]], l.cols)) +
    input("lcd", "scroll_speed", { label: "Scroll speed", unit: "ms" }) +
    lcdToggleRow("activity_interrupt", "Interrupt on activity", "ON", "OFF"));
  const help = note("Lines fill in from <b>{tokens}</b> — e.g. <code>{callsign}</code>, <code>{status}</code>, <code>{lh_call}</code>. A line wider than the panel scrolls. Characters outside plain ASCII show as <code>?</code>.");
  const disabled = l.enabled ? "" : note("The driver is <b>disabled</b> — pages are saved but nothing is drawn until you enable it above.");
  const pages = (l.pages || []).map((p, i) => pageCard(p, i, rows)).join("");
  const add = `<button type="button" class="btn ghost mini-btn" id="lcd-add-page">+ ADD PAGE</button>`;
  return `<div class="grid2">${panel}<div class="stack">${help}${disabled}</div></div>` +
    `<div class="stack" style="margin-top:16px;">${pages || note("No pages yet — add one to show something on the panel.")}${add}</div>`;
}

// ensureLcdLine pads a page's lines array so index ri is assignable.
function ensureLcdLine(pi, ri) {
  const p = edit.lcd.pages[pi];
  while (p.lines.length <= ri) p.lines.push("");
}

// insertLcdToken drops {token} at the caret of the active line input on page pi
// (or its first row), then restores focus and caret past the inserted token.
function insertLcdToken(pi, token) {
  let ri = 0;
  if (lcdActive && lcdActive.page === pi) ri = lcdActive.row;
  ensureLcdLine(pi, ri);
  const inputEl = document.querySelector(`input[data-lcdline="${pi}"][data-lcdrow="${ri}"]`);
  const cur = edit.lcd.pages[pi].lines[ri] || "";
  let pos = cur.length;
  if (inputEl && inputEl.selectionStart != null) pos = inputEl.selectionStart;
  const ins = "{" + token + "}";
  edit.lcd.pages[pi].lines[ri] = cur.slice(0, pos) + ins + cur.slice(pos);
  dirty.add("lcd");
  renderPanel();
  refreshActions();
  const after = document.querySelector(`input[data-lcdline="${pi}"][data-lcdrow="${ri}"]`);
  if (after) { after.focus(); const c = pos + ins.length; after.setSelectionRange(c, c); }
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

const enPill = (type, n) => { const on = !!(n && n.enabled); return `<button type="button" class="pill ${on ? "on" : "off"}" data-neten="${type}" aria-pressed="${on}" aria-label="${esc(type)} network enabled">${on ? "ENABLED" : "DISABLED"}</button>`; };
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
      <div class="toggle-row"><span class="name">DMR Roaming Beacon</span><button type="button" class="pill ${d.beacons ? "on" : "off"}" data-toggle="dmr.beacons" aria-pressed="${!!d.beacons}" aria-label="DMR Roaming Beacon">${d.beacons ? "ON" : "OFF"}</button></div>
      ${row("DMR Color Code", `<select data-sec="dmr" data-key="color_code">${ccOpts}</select>`)}
      <div class="toggle-row"><span class="name">DMR EmbeddedLCOnly</span><button type="button" class="pill ${d.embedded_lc_only ? "on" : "off"}" data-toggle="dmr.embedded_lc_only" aria-pressed="${!!d.embedded_lc_only}" aria-label="DMR EmbeddedLCOnly">${d.embedded_lc_only ? "ON" : "OFF"}</button></div>
      <div class="toggle-row"><span class="name">DMR DumpTAData</span><button type="button" class="pill ${d.dump_ta_data ? "on" : "off"}" data-toggle="dmr.dump_ta_data" aria-pressed="${!!d.dump_ta_data}" aria-label="DMR DumpTAData">${d.dump_ta_data ? "ON" : "OFF"}</button></div>
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
      ${slotSelect(r.slot, `data-rtslot="${j}" aria-label="Route ${j + 1} time slot"`)}
      <input class="mini" data-rttg="${j}" value="${esc(r.tg)}" placeholder="dialed TG" aria-label="Route ${j + 1} dialed talkgroup">
      <span class="arr" aria-hidden="true">→</span>
      <select class="mini" data-rtnet="${j}" aria-label="Route ${j + 1} gateway">${netOpts(r.network)}</select>
      <button class="netdel" data-rtdel="${j}" aria-label="Remove route ${j + 1}">✕</button>
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

// The Gateways tab: one card per cross-mode transcoding bridge (MMDVM_CM). Each
// card has its own Enable toggle — the bridge runs as its own daemon, so enabling
// it renders its INI and starts its unit on Apply (render.go RenderTargets). The
// two DMR-master bridges (YSF2DMR, NXDN2DMR) add a master + redacted password +
// options + target TG; the others are minimal. pwRow renders a write-only-secret
// input (blank keeps the stored one), matching the D-Star ircDDB password.
function pwRow(sec, hasPw) {
  const val = (edit[sec] || {}).password || "";
  return row("DMR master password", `<input data-sec="${esc(sec)}" data-key="password" type="password" value="${esc(val)}" placeholder="${hasPw ? "•••••• unchanged" : "master password"}">`);
}
function panelGateways() {
  const y2d = edit.ysf2dmr || {}, n2d = edit.nxdn2dmr || {};
  const ysf2dmr = card("YSF2DMR — YSF → DMR",
    toggle("ysf2dmr", "enable", "Bridge", "ENABLED", "DISABLED") +
    input("ysf2dmr", "dmr_id", { label: "CCS7 / DMR ID" }) +
    input("ysf2dmr", "master", { label: "DMR master (address)" }) +
    pwRow("ysf2dmr", y2d.has_password) +
    input("ysf2dmr", "tg", { label: "YSF2DMR talkgroup", accent: true }) +
    input("ysf2dmr", "options", { label: "DMR options (advanced)" }));
  const nxdn2dmr = card("NXDN2DMR — NXDN → DMR",
    toggle("nxdn2dmr", "enable", "Bridge", "ENABLED", "DISABLED") +
    input("nxdn2dmr", "dmr_id", { label: "CCS7 / DMR ID" }) +
    input("nxdn2dmr", "master", { label: "DMR master (address)" }) +
    pwRow("nxdn2dmr", n2d.has_password) +
    input("nxdn2dmr", "tg", { label: "DMR talkgroup", accent: true }) +
    input("nxdn2dmr", "nxdn_tg", { label: "NXDN talkgroup" }) +
    input("nxdn2dmr", "options", { label: "DMR options (advanced)" }));
  const dmr2ysf = card("DMR2YSF — DMR → YSF",
    toggle("dmr2ysf", "enable", "Bridge", "ENABLED", "DISABLED") +
    input("dmr2ysf", "dmr_id", { label: "CCS7 / DMR ID" }) +
    input("dmr2ysf", "default_tg", { label: "Default DMR talkgroup", accent: true }));
  const ysf2nxdn = card("YSF2NXDN — YSF → NXDN",
    toggle("ysf2nxdn", "enable", "Bridge", "ENABLED", "DISABLED") +
    input("ysf2nxdn", "nxdn_id", { label: "NXDN ID" }) +
    input("ysf2nxdn", "tg", { label: "NXDN talkgroup", accent: true }));
  const dmr2nxdn = card("DMR2NXDN — DMR → NXDN",
    toggle("dmr2nxdn", "enable", "Bridge", "ENABLED", "DISABLED") +
    input("dmr2nxdn", "dmr_id", { label: "CCS7 / DMR ID" }) +
    input("dmr2nxdn", "nxdn_id", { label: "NXDN ID" }));
  const hint = note("Each bridge is a standalone transcoding daemon. Enabling one renders its INI and starts its unit on Apply; the DMR-master bridges log into their own master (password stored on the node, never shown). A bridge and the gateway it borrows loopback ports from can't run at once.");
  return `<div class="grid2"><div class="stack">${ysf2dmr}${nxdn2dmr}</div><div class="stack">${dmr2ysf}${ysf2nxdn}${dmr2nxdn}</div></div>${hint}`;
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

// The POCSAG panel splits into the "modes" enable + the "pocsag" store section
// (paging frequency + DAPNETGateway login/filters). The AuthKey is a redacted
// secret: it starts blank (blank = keep the stored one) and has_auth_key drives
// the placeholder, exactly like the ircDDB password.
function panelPocsag() {
  const p = edit.pocsag || (edit.pocsag = {});
  const paging = card("PAGING CHANNEL",
    toggle("modes", "pocsag", "POCSAG", "ENABLED", "DISABLED") +
    input("pocsag", "frequency", { label: "Paging frequency", kind: "mhz", unit: "MHz", accent: true }));
  const dapnet = card("DAPNET LOGIN",
    input("pocsag", "server", { label: "DAPNET server" }) +
    input("pocsag", "callsign", { label: "Callsign (blank = station callsign)" }) +
    row("AuthKey", `<input data-sec="pocsag" data-key="auth_key" type="password" value="${esc(p.auth_key || "")}" placeholder="${p.has_auth_key ? "•••••• unchanged" : "from the DAPNET portal"}">`));
  const filters = card("RIC FILTERS (OPTIONAL)",
    input("pocsag", "whitelist", { label: "Whitelist (comma-separated RICs)" }) +
    input("pocsag", "blacklist", { label: "Blacklist (comma-separated RICs)" }));
  const hint = note("DAPNETGateway will not connect until a valid AuthKey is set (get one from the DAPNET web portal). Leave whitelist/blacklist blank to pass all RICs.");
  return `<div class="grid2"><div class="stack">${paging}${filters}</div>${dapnet}</div>${hint}`;
}

// FM (analog) has no gateway daemon — the panel edits the "modes" enable + the
// "fm" store section only. Access mode is a select over MMDVM-Host's 0..3 set.
function panelFm() {
  const f = edit.fm || (edit.fm = {});
  const amVal = f.access_mode || "1";
  const amSel = `<select data-sec="fm" data-key="access_mode">` +
    [["0", "0 — Carrier access with COS"], ["1", "1 — CTCSS access, no COS"],
     ["2", "2 — CTCSS access with COS"], ["3", "3 — CTCSS start, then carrier"]]
      .map(([v, l]) => `<option value="${v}"${v === amVal ? " selected" : ""}>${esc(l)}</option>`).join("") + `</select>`;
  const access = card("ACCESS",
    toggle("modes", "fm", "FM", "ENABLED", "DISABLED") +
    input("fm", "ctcss", { label: "CTCSS tone", unit: "Hz", accent: true }) +
    row("Access mode", amSel));
  const timing = card("TIMING",
    input("fm", "timeout", { label: "Timeout", unit: "sec" }) +
    input("fm", "kerchunk_time", { label: "Kerchunk time", unit: "sec" }));
  const audio = card("AUDIO LEVELS",
    input("fm", "rf_audio_boost", { label: "RF audio boost" }) +
    input("fm", "ext_audio_boost", { label: "Network audio boost" }));
  return `<div class="grid2">${access}<div class="stack">${timing}${audio}</div></div>`;
}

// enhanceA11y wires every rendered form control to an accessible name so screen
// readers announce it and axe-core's label/select-name rules pass. Rows are
// built as `<label>text</label><control>` without a `for=` (the control's id is
// generated), so we associate them after render; any control still nameless
// (route/table fields) falls back to its placeholder. Run after every render.
let a11yCounter = 0;
function enhanceA11y() {
  const box = document.getElementById("panels");
  box.querySelectorAll(".row").forEach((rowEl) => {
    const label = rowEl.querySelector(":scope > label");
    const ctrl = rowEl.querySelector("input, select, textarea");
    if (!label || !ctrl) return;
    // A <label for> can only target labelable elements; toggle buttons carry
    // their own aria-label, so skip them here.
    if (!ctrl.id) ctrl.id = "wp-f-" + (a11yCounter++);
    if (!label.getAttribute("for")) label.setAttribute("for", ctrl.id);
  });
  box.querySelectorAll("input, select, textarea").forEach((ctrl) => {
    if (namedControl(ctrl)) return;
    const ph = ctrl.getAttribute("placeholder");
    if (ph) ctrl.setAttribute("aria-label", ph);
  });
}
function namedControl(c) {
  if (c.getAttribute("aria-label") || c.getAttribute("aria-labelledby") || c.getAttribute("title")) return true;
  if (c.closest("label")) return true;
  if (c.id && document.querySelector(`label[for="${CSS.escape(c.id)}"]`)) return true;
  return false;
}

function renderPanel() {
  const c = state.config || {};
  const box = document.getElementById("panels");
  switch (state.tab) {
    case "general":      box.innerHTML = panelGeneral(); break;
    case "setup":        box.innerHTML = panelDisplay(); break;
    case "lcd":          box.innerHTML = panelLCD(); break;
    case "dmr":          box.innerHTML = panelDmr(); break;
    case "dstar":        box.innerHTML = panelDStar(); break;
    case "ysf":          box.innerHTML = panelYSF(); break;
    case "p25":          box.innerHTML = panelP25(); break;
    case "nxdn":         box.innerHTML = panelNXDN(); break;
    case "m17":          box.innerHTML = panelM17(); break;
    case "pocsag":       box.innerHTML = panelPocsag(); break;
    case "fm":           box.innerHTML = panelFm(); break;
    case "modes":        box.innerHTML = panelModes(); break;
    case "brandmeister": box.innerHTML = panelBrandmeister(); break;
    case "expert":       box.innerHTML = panelExpert(c, state.health); break;
    case "gateways":     box.innerHTML = panelGateways(); break;
    case "network":      box.innerHTML = panelPending("Network & Wi-Fi"); break;
    default:             box.innerHTML = "";
  }
  enhanceA11y();
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
    b.setAttribute("role", "status");
    b.setAttribute("aria-live", "polite");
    b.style.cssText = "margin:0 0 18px; padding:11px 14px; border-radius:8px; font-family:var(--mono); font-size:12px;";
    document.getElementById("panels").before(b);
  }
  b.setAttribute("role", kind === "bad" ? "alert" : "status");
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
        : sec === "pocsag" ? cleanPocsag(edit.pocsag)
        : (sec === "ysf2dmr" || sec === "nxdn2dmr") ? cleanBridge(edit[sec])
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
    const on = t.id === state.tab;
    const item = el("button", "nav-item" + (on ? " on" : ""));
    item.type = "button";
    if (on) item.setAttribute("aria-current", "page");
    item.setAttribute("aria-label", t.label + " — " + t.sub);
    item.innerHTML = `<div class="bar" aria-hidden="true"></div><div class="tag" aria-hidden="true">${esc(t.tag)}</div><div><div class="label">${esc(t.label)}</div><div class="sub">${esc(t.sub)}</div></div>`;
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
    s.type = "button";
    s.title = th.key;
    s.setAttribute("aria-label", th.key + " theme");
    s.setAttribute("aria-pressed", String(th.key === cur));
    s.innerHTML = `<span class="dot" style="background:${th.color}; box-shadow:0 0 7px ${th.color};" aria-hidden="true"></span>`;
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
    d.title = m.name + (m.enabled ? " enabled" : " disabled");
    d.setAttribute("aria-label", m.name + (m.enabled ? " enabled" : " disabled"));
    d.innerHTML = `<span class="d" aria-hidden="true"></span><span class="a">${esc(m.key.toUpperCase())}</span>`;
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
  // LCD rows/cols selects — rows changes the line-input count, so re-render.
  if (t.dataset.lcdDim != null) {
    setField("lcd", t.dataset.lcdDim, t.value);
    if (t.dataset.lcdDim === "rows") renderPanel();
    return;
  }
  // LCD page name / duration.
  if (t.dataset.lcdpage != null) {
    edit.lcd.pages[+t.dataset.lcdpage][t.dataset.lcdkey] = t.value;
    dirty.add("lcd"); refreshActions();
    return;
  }
  // LCD page line: update the model and refresh just this page's token warning.
  if (t.dataset.lcdline != null) {
    const pi = +t.dataset.lcdline, ri = +t.dataset.lcdrow;
    ensureLcdLine(pi, ri);
    edit.lcd.pages[pi].lines[ri] = t.value;
    dirty.add("lcd"); updatePageWarning(pi); refreshActions();
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
  // LCD token palette: insert {token} at the active line's caret.
  const tok = e.target.closest("[data-lcdtoken]");
  if (tok) { insertLcdToken(+tok.dataset.lcdpageidx, tok.dataset.lcdtoken); return; }
  // LCD per-page enable toggle.
  const lpe = e.target.closest("[data-lcdpageen]");
  if (lpe) { const p = edit.lcd.pages[+lpe.dataset.lcdpageen]; p.enabled = !p.enabled; dirty.add("lcd"); renderPanel(); refreshActions(); return; }
  // LCD remove page.
  const lpd = e.target.closest("[data-lcdpagedel]");
  if (lpd) { edit.lcd.pages.splice(+lpd.dataset.lcdpagedel, 1); dirty.add("lcd"); renderPanel(); refreshActions(); return; }
  // LCD add page.
  if (e.target.id === "lcd-add-page") {
    (edit.lcd.pages = edit.lcd.pages || []).push({ enabled: true, name: "Page " + (edit.lcd.pages.length + 1), duration: "8", lines: [] });
    dirty.add("lcd"); renderPanel(); refreshActions();
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
// Track the focused LCD line input so the token palette inserts into the right
// row even after the click moves focus to the button.
document.getElementById("panels").addEventListener("focusin", (e) => {
  const t = e.target;
  if (t && t.dataset && t.dataset.lcdline != null) lcdActive = { page: +t.dataset.lcdline, row: +t.dataset.lcdrow };
});
document.getElementById("btn-apply").onclick = apply;
document.getElementById("btn-reset").onclick = reset;

renderNav();
renderThemes();
selectTab((location.hash || "").slice(1) || "general");
load();
