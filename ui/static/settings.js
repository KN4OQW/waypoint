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
  { id: "profiles",     tag: "PF", label: "Profiles",     sub: "Saved Setups",         crumb: "SYSTEM / PROFILES",       title: "Connection Profiles",   desc: "Named snapshots of your mode & network setup — save the current one, switch to another in a click, or carry a setup between nodes as a file. Callsign, frequencies and calibration are never part of a profile, so switching can't change your identity or detune the radio." },
  { id: "gateways",     tag: "GW", label: "Gateways",     sub: "Cross-Mode Routing",   crumb: "BRIDGES / GATEWAYS",      title: "Cross-Mode Gateways",   desc: "Cross-mode routing is being redesigned as a bus system (RFC-0003)." },
  { id: "network",      tag: "NW", label: "Network",      sub: "Wi-Fi & IP",           crumb: "SYSTEM / NETWORK",        title: "Network & Wi-Fi",       desc: "Wireless credentials and IP configuration for the host device." },
  { id: "station",      tag: "ST", label: "Station",      sub: "History & Beacon",     crumb: "SYSTEM / STATION",        title: "Station Settings",      desc: "Node-wide operating policy: how long the persistent last-heard / event history is kept (pruned nightly), and — coming soon — automatic callsign identification." },
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
let overridesData = null;   // GET /api/overrides — the override layer's effective records (read-only, RFC-0005)
let profiles = null;        // saved connection profiles from /api/profiles (RFC-0006)
let profileBusy = false;    // an activate/save/import is in flight (disables the buttons)
let importScan = null;      // last /api/import/scan result {report, preview} (RFC-0007)
let importInput = null;     // remembered scan input to replay on Import: {dir} or {files: FileList}
let importBusy = false;     // a scan/import is in flight
let netStatus = null;       // live host-network state from /api/network/status (read-only)
let netEdit = null;          // working copy of /api/network/config (editable)
let netDirty = false;        // unsaved connection/VLAN edits (guarded Apply Network)
let netHostDirty = false;    // unsaved host/NTP edits (direct Apply Host Settings)
let netScanResults = [];     // cached /api/network/wifi/scan for the join picker
let netTimezones = [];       // cached /api/network/timezones for the tz datalist
let netCountdown = null;     // interval handle for the confirm-or-revert countdown bar
let netApplying = false;     // an Apply Network is in flight
let netApplyingHost = false; // an Apply Host Settings is in flight

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
    lcd: lcdFrom(c.lcd || {}),
    dmr:     { color_code: d.color_code, id: d.id, embedded_lc_only: !!d.embedded_lc_only, dump_ta_data: !!d.dump_ta_data, beacons: !!d.beacons, self_only: !!d.self_only },
    dmrnet:  { slot1: !!d.slot1, slot2: !!d.slot2 },
    modes:   Object.fromEntries((c.modes || []).map((m) => [m.key, !!m.enabled])),
    ysf:     ysfFrom(c.ysf || {}),
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
    // Event-history retention (Station Settings tab). Kept as a number so the PUT
    // body carries retention_days as JSON number, not string (the store field is
    // an int). Falls back to the 7-day default if the view somehow omits it.
    history: { retention_days: (c.history || {}).retention_days ?? 7 },
    // Mode buses (RFC-0003): buses[] and their attachments. Neither carries a
    // secret — a DMR attachment authenticates through an existing network named by
    // credentials_ref, never its own password (assert-the-shape: no password field
    // exists here). tg_map is expanded to editable rows and folded back on save.
    buses: (c.buses || []).map((b) => ({ id: b.id, name: b.name || "", enabled: !!b.enabled })),
    attachments: (c.attachments || []).map(attachFrom),
    // Bus LAN peering (RFC-0016): the redacted peer rows (fingerprints visible,
    // cert/key never) and the remote (via-peer) attachments. Discovery + pending
    // pairings are fetched dynamically (they are not config sections).
    peers: (c.peers || []).map((p) => ({ id: p.id, name: p.name || "", host: p.host || "", port: p.port || "", mdns_instance: p.mdns_instance || "", state: p.state || "", fingerprint: p.fingerprint || "", has_certificate: !!p.has_certificate, has_key: !!p.has_key })),
    remote_attachments: (c.remote_attachments || []).map((r) => ({ bus_id: r.bus_id, peer_id: r.peer_id, mode: r.mode, target: r.target || "", default_tg: r.default_tg || "", slot: r.slot || "", tg: r.tg || "", id: r.id || "", default_id: r.default_id || "" })),
  };
  dirty = new Set();
  refreshActions();
}

// The YSF view is flat (mode params + gateway settings); it splits back into two
// store sections: "ysf" (MMDVM-Host [System Fusion] mode params) and "ysfgw"
// (YSFGateway.ini). This mirrors p25/p25gw and nxdn/nxdngw. TXHang/ModeHang default
// to the values MMDVM-Host renders when blank.
function ysfFrom(y) {
  return {
    self_only: !!y.self_only, low_deviation: !!y.low_deviation,
    tx_hang: y.tx_hang || "4", mode_hang: y.mode_hang || "20",
    remote_gateway: !!y.remote_gateway,
  };
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
    linger_secs: l.linger_secs || "3",
    pages: (l.pages || []).map((p) => ({
      enabled: p.enabled !== false,
      name: p.name || "",
      duration: p.duration || "8",
      interrupt: !!p.interrupt,
      lines: (p.lines || []).slice(),
    })),
  };
}

// LCD_TOKEN_HELP is the single source of truth for the token palette, the legend,
// client-side validation, and the preview. Each entry documents the token and its
// data source; it mirrors the renderer's grounded token set (internal/lcd/tokens.go)
// so the UI never offers a token the driver can't expand. `sample` feeds the live
// preview (a representative "active DMR call" snapshot).
const LCD_TOKEN_HELP = [
  ["callsign", "Station callsign (config)", "KN4OQW"],
  ["dmr_id", "DMR ID (config)", "3180202"],
  ["ip", "Node's LAN IPv4 address", "192.168.1.50"],
  ["hostname", "Node hostname", "waypoint"],
  ["version", "Waypoint version", "1.0"],
  ["freq_rx", "RX frequency, MHz (modem config)", "433.1250"],
  ["freq_tx", "TX frequency, MHz (modem config)", "433.1250"],
  ["time", "Clock, HH:MM", "15:04"],
  ["date", "Date, YYYY-MM-DD", "2026-07-14"],
  ["uptime", "Time since the daemon started", "1h30m"],
  ["mode", "Active mode, else IDLE", "DMR"],
  ["modes", "Enabled modes, space-joined", "DMR YSF"],
  ["status", "Activity line, else Listening", "RX DMR TG91 W1ABC"],
  ["source", "Caller now, else last heard", "W1ABC"],
  ["tg", "Talkgroup now, else last heard", "TG91"],
  ["rssi", "Signal of the last transmission", "-70"],
  ["ber", "Bit-error rate of the last transmission", "0.5%"],
  ["lh_call", "Last heard callsign", "W1ABC"],
  ["lh_tg", "Last heard talkgroup", "TG91"],
  ["lh_mode", "Last heard mode", "DMR"],
  ["lh_ber", "Last heard bit-error rate", "0.5%"],
  ["lh_rssi", "Last heard RSSI, dBm", "-70"],
  ["lh_ago", "Time since the last transmission", "30s"],
];
const LCD_TOKENS = LCD_TOKEN_HELP.map((t) => t[0]);
const LCD_SAMPLE = LCD_TOKEN_HELP.reduce((m, t) => { m[t[0]] = t[2]; return m; }, {});
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

// lcdExpandLine mirrors the Go renderer (internal/lcd): expand {tokens} against a
// sample snapshot (unknown → blank), strip non-ASCII to "?", then truncate/pad to
// exactly cols. Used for the client-side preview only.
function lcdExpandLine(line, cols) {
  let out = String(line || "").replace(/\{([a-z0-9_]+)\}/g, (m, name) =>
    Object.prototype.hasOwnProperty.call(LCD_SAMPLE, name) ? LCD_SAMPLE[name] : "");
  out = out.replace(/[^\x20-\x7e]/g, "?");
  if (out.length > cols) return out.slice(0, cols);
  return out + " ".repeat(cols - out.length);
}

// lcdPreviewText renders a page to rows lines of cols columns, exactly as the
// panel would show it at rest (no scroll) — a faithful geometry-matching preview.
function lcdPreviewText(page, rows, cols) {
  const lines = [];
  for (let i = 0; i < rows; i++) lines.push(lcdExpandLine((page.lines || [])[i] || "", cols));
  return lines.join("\n");
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

function pageCard(p, i, rows, cols, total) {
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
    LCD_TOKEN_HELP.map(([tk, desc]) => `<button type="button" class="lcd-tok" data-lcdtoken="${esc(tk)}" data-lcdpageidx="${i}" title="${esc(desc)} — inserts {${esc(tk)}}">{${esc(tk)}}</button>`).join("") + `</div>`;
  const preview = `<div class="lcd-preview">` +
    `<div class="lcd-preview-label" id="lcd-pv-label-${i}">Preview (${esc(cols)}×${esc(String(rows))})</div>` +
    `<pre class="lcd-screen" data-lcdpreview="${i}" role="group" aria-labelledby="lcd-pv-label-${i}">${esc(lcdPreviewText(p, rows, parseInt(cols, 10) || 20))}</pre></div>`;
  const upDis = i === 0 ? " disabled aria-disabled=\"true\"" : "";
  const dnDis = i === total - 1 ? " disabled aria-disabled=\"true\"" : "";
  return `<section class="card lcd-page">
      <div class="card-head lcd-pagehead">
        <button type="button" class="lcd-move" data-lcdmove="up" data-lcdpageidx="${i}" aria-label="Move page ${i + 1} up"${upDis}>▲</button>
        <button type="button" class="lcd-move" data-lcdmove="down" data-lcdpageidx="${i}" aria-label="Move page ${i + 1} down"${dnDis}>▼</button>
        <input class="lcd-pagename" data-lcdpage="${i}" data-lcdkey="name" value="${esc(p.name || "")}" placeholder="Page name" aria-label="Page ${i + 1} name">
        <button type="button" class="pill ${p.enabled ? "on" : "off"}" data-lcdpageen="${i}" aria-pressed="${p.enabled ? "true" : "false"}" aria-label="Page ${i + 1} enabled">${p.enabled ? "ENABLED" : "DISABLED"}</button>
        <button type="button" class="pill ${p.interrupt ? "on" : "off"}" data-lcdpageint="${i}" aria-pressed="${p.interrupt ? "true" : "false"}" aria-label="Page ${i + 1} interrupt on activity" title="Take over the panel on TX/RX, then resume rotation">${p.interrupt ? "INTERRUPT" : "ROTATE"}</button>
        <span class="lcd-dur"><input class="mini" data-lcdpage="${i}" data-lcdkey="duration" value="${esc(p.duration || "")}" inputmode="numeric" aria-label="Page ${i + 1} hold seconds"> s</span>
        <button type="button" class="netdel" data-lcdpagedel="${i}" aria-label="Remove page ${i + 1}">✕</button>
      </div>
      ${lines}${warn}${preview}${palette}
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

// lcdLegend is the token reference: every token, what it shows, and its source.
// It is generated from LCD_TOKEN_HELP so it can never drift from the palette or
// the renderer. Rendered as a real <dl> inside <details> for accessible reading.
function lcdLegend() {
  const items = LCD_TOKEN_HELP.map(([tk, desc]) =>
    `<dt>{${esc(tk)}}</dt><dd>${esc(desc)}</dd>`).join("");
  return `<details class="lcd-legend"><summary>TOKEN REFERENCE</summary><dl>${items}</dl></details>`;
}

function panelLCD() {
  const l = edit.lcd || (edit.lcd = lcdFrom({}));
  const rows = Math.max(1, parseInt(l.rows, 10) || 4);
  const cols = l.cols || "20";
  const panel = card("PANEL",
    lcdToggleRow("enabled", "Driver enabled", "ENABLED", "DISABLED") +
    input("lcd", "i2c_bus", { label: "I2C bus" }) +
    input("lcd", "i2c_address", { label: "I2C address", accent: true }) +
    row("Rows", lcdSelect("rows", [["2", "2 rows"], ["4", "4 rows"]], l.rows)) +
    row("Columns", lcdSelect("cols", [["16", "16 columns"], ["20", "20 columns"]], l.cols)) +
    input("lcd", "scroll_speed", { label: "Scroll speed", unit: "ms" }) +
    lcdToggleRow("activity_interrupt", "Interrupt on activity", "ON", "OFF") +
    input("lcd", "linger_secs", { label: "Interrupt linger", unit: "s" }));
  const help = note("Lines fill in from <b>{tokens}</b> — e.g. <code>{callsign}</code>, <code>{status}</code>, <code>{source}</code>. A line wider than the panel scrolls; characters outside plain ASCII show as <code>?</code>. A page may have at most as many lines as the panel has rows.");
  const disabled = l.enabled ? "" : note("The driver is <b>disabled</b> — pages are saved but nothing is drawn until you enable it above.");
  const pages = (l.pages || []).map((p, i) => pageCard(p, i, rows, cols, (l.pages || []).length)).join("");
  const add = `<button type="button" class="btn ghost mini-btn" id="lcd-add-page">+ ADD PAGE</button>`;
  return `<div class="grid2">${panel}<div class="stack">${help}${lcdLegend()}${disabled}</div></div>` +
    `<div class="stack" style="margin-top:16px;">${pages || note("No pages yet — add one to show something on the panel.")}${add}</div>`;
}

// updatePagePreview refreshes one page's live preview in place (no re-render) so
// typing in a line input never steals focus, mirroring updatePageWarning.
function updatePagePreview(i) {
  const el = document.querySelector(`[data-lcdpreview="${i}"]`);
  if (!el) return;
  const l = edit.lcd || {};
  const rows = Math.max(1, parseInt(l.rows, 10) || 4);
  el.textContent = lcdPreviewText(l.pages[i], rows, parseInt(l.cols, 10) || 20);
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
let dmrTGs = [];     // cached /api/dmr/talkgroups, for the searchable TG picker (RFC-0010)

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
  // A searchable TG picker (RFC-0010): the datalist option value embeds both the
  // number and the name ("3112 · Texas Statewide") so native typeahead filters on
  // either; the input handler extracts the leading number for storage. Typing a
  // few characters of the name selects the TG — no thousand-row dropdown.
  const tgOpts = dmrTGs.map((t) => `<option value="${esc(t.id + " · " + t.name)}"></option>`).join("");
  const rows = routes.map((r, j) => `
    <div class="route-row">
      ${slotSelect(r.slot, `data-rtslot="${j}" aria-label="Route ${j + 1} time slot"`)}
      <input class="mini" list="dmr-tgs" data-rttg="${j}" value="${esc(tgDisplay(r.tg))}" placeholder="dialed TG — type a name or number" aria-label="Route ${j + 1} dialed talkgroup (type to search)">
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
      <datalist id="dmr-tgs">${tgOpts}</datalist>
      ${body}
      <button class="btn ghost mini-btn" id="route-add"${nets.length ? "" : " disabled"}>+ ADD ROUTE</button>
    </div>`;
}

// tgDisplay renders a stored TG number as "3112 · Texas Statewide" when the name
// is known, so a saved route reads legibly; unknown/blank falls back to the raw
// value (RFC-0010).
function tgDisplay(tg) {
  if (!tg) return "";
  const hit = dmrTGs.find((t) => t.id === String(tg));
  return hit ? `${hit.id} · ${hit.name}` : String(tg);
}
// tgNumber extracts the leading TG number from a picker value ("3112 · Texas
// Statewide" -> "3112"), so routing stores the number the gateway needs.
function tgNumber(v) {
  const m = /^\s*(\d+)/.exec(v || "");
  return m ? m[1] : (v || "").trim();
}

function panelExpert(c, h) {
  const rows = card("VERSIONS",
    `<div class="row"><label>Dashboard (waypointd)</label><input value="${esc((h && h.version) || "—")}" readonly></div>` +
    `<div class="row"><label>Config store</label><input value="${esc((c.sources && c.sources.store) || "—")}" readonly></div>`);
  return `<div class="grid2">${rows}${note("Raw INI editing and power controls land in a later slice. Config now lives in the store; the INIs are regenerated on Apply — <a href='https://github.com/KN4OQW/waypoint/issues/29'>waypoint#29</a>.")}</div>${panelImport()}${panelOverrides()}`;
}

// panelImport is the Pi-Star / WPSD migration surface (RFC-0007 / issue #4): point
// Waypoint at a mounted card (a directory path) or upload the incumbent config
// files, Scan for a preview + report, then Import to bulk-write the store.
function panelImport() {
  const dis = importBusy ? " disabled" : "";
  const input = card("IMPORT FROM PI-STAR / WPSD", `
    <div class="row"><label>Mounted card path</label>
      <input id="import-dir" placeholder="/mnt/sdcard  (or /media/…)" aria-label="Mounted incumbent card path"></div>
    <div style="display:flex; gap:12px; align-items:center; margin-top:6px; flex-wrap:wrap;">
      <button type="button" id="import-scan-dir"${dis} style="padding:8px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:6px;">SCAN DIRECTORY</button>
      <label class="import-upload" style="font-family:var(--mono); font-size:12px; cursor:pointer; text-decoration:underline;">
        …or upload config files<input id="import-files" type="file" multiple style="display:none;"></label>
    </div>
    ${note("Copy the incumbent's <code>/etc/mmdvmhost</code>, <code>/etc/dmrgateway</code> and other gateway config files off the old card, or mount the card and give its path. Nothing is written until you press <b>Import</b>.")}`);

  const result = importScan ? importReport(importScan) : "";
  return `<div style="margin-top:14px;">${input}${result}</div>`;
}

function importReport(s) {
  const rep = s.report || {};
  const files = (rep.files || []).map((f) =>
    `<div class="row"><label>${esc(f.role)}</label><span style="font-family:var(--mono); font-size:12px;">${f.found ? "✓ " + esc(f.name) : "— not found"}</span></div>`).join("");
  const modes = (rep.modes || []).length ? esc((rep.modes || []).join(", ")) : "—";
  const nets = (rep.networks || []).map((n) =>
    `<div class="row"><label>${esc(n.name)}</label><span style="font-family:var(--mono); font-size:12px;">${esc(n.type)}${n.custom ? " · <b>custom routing preserved</b>" : ""}${n.enabled ? "" : " · disabled"}</span></div>`).join("") || note("No DMR networks found.");
  const unmapped = (rep.unmapped || []).length
    ? `<div class="card"><div class="card-head"><span class="sq"></span><span class="t">WON'T CARRY OVER</span></div>${(rep.unmapped || []).map((u) => `<div class="row"><label>${esc(u.file)} · ${esc(u.section)}</label><span style="font-family:var(--mono); font-size:12px;">${esc(u.what)}</span></div>`).join("")}${note("These incumbent features aren't modeled yet — reconfigure them in Waypoint after importing.")}</div>`
    : note("Everything found maps to Waypoint — nothing left behind.");
  const dis = importBusy ? " disabled" : "";
  const summary = card("SCAN RESULT",
    `<div class="row"><label>Detected</label><span style="font-family:var(--mono); font-size:12px;">${esc(rep.platform || "unknown")}</span></div>` +
    `<div class="row"><label>Modes</label><span style="font-family:var(--mono); font-size:12px;">${modes}</span></div>` + files);
  const apply = `<div style="display:flex; gap:12px; align-items:center; margin-top:10px;">
      <button type="button" id="import-apply"${dis} style="padding:9px 20px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">IMPORT INTO STORE</button>
      <span class="note" style="margin:0;">Overwrites your current mode &amp; network config with the scanned values. Passwords carry over from the card; review, then <b>Apply</b> to go live.</span>
    </div>`;
  return `<div class="stack" style="margin-top:12px;">${summary}${card("DMR NETWORKS", nets)}${unmapped}</div>${apply}`;
}

// panelOverrides renders the read-only Override layer view (RFC-0005 / issue #2):
// the drop-in fragments that merge last into the generated INIs and survive every
// update. Data comes from GET /api/overrides, loaded lazily when the Expert tab
// opens. "Visible, not fought" — the operator sees exactly what their overrides
// change and which fragment wins.
function panelOverrides() {
  const d = overridesData;
  if (!d) return card("OVERRIDES", note("Loading override layer…"));
  const dirLine = note(`Override drop-ins live under <code>${esc(d.dir || "—")}/&lt;daemon&gt;.d/*.conf</code> and host-file hooks under <code>&lt;hostfile&gt;.prepend.d/</code> · <code>.append.d/</code>. They merge last into the generated files and are never touched by an update (<a href="https://github.com/KN4OQW/waypoint/issues/2">waypoint#2</a>).`);
  const warn = (d.warnings && d.warnings.length)
    ? note(`<b>${d.warnings.length} malformed override line(s) ignored:</b><br>${d.warnings.map(esc).join("<br>")}`)
    : "";
  const list = d.overrides || [];
  if (!list.length) {
    return card("OVERRIDES", dirLine + note("No overrides active — the generated configuration is exactly what the store renders.") + warn);
  }
  // Group by daemon so each generated file's overrides read together.
  const byDaemon = {};
  list.forEach((o) => { (byDaemon[o.daemon] = byDaemon[o.daemon] || []).push(o); });
  const groups = Object.keys(byDaemon).sort().map((daemon) => {
    const rows = byDaemon[daemon].map(overrideRow).join("");
    return `<div class="card"><div class="card-head"><span class="sq"></span><span class="t">${esc(daemon)}</span></div>${rows}</div>`;
  }).join("");
  return card("OVERRIDES", dirLine + warn) + `<div class="stack">${groups}</div>`;
}

// overrideRow renders one effective override as a read-only status line:
// section · key, the rendered→effective transition (or REMOVED / ADDED), and the
// winning fragment filename (the provenance).
function overrideRow(o) {
  let change;
  if (o.unset) change = `<s>${esc(o.old)}</s> <b>REMOVED</b>`;
  else if (o.added) change = `<b>${esc(o.new)}</b> <span class="note" style="display:inline">ADDED</span>`;
  else change = `<s>${esc(o.old)}</s> → <b>${esc(o.new)}</b>`;
  const label = `[${o.section}] ${o.key}`;
  return `<div class="row"><label>${esc(label)}</label><span style="font-family:var(--mono); font-size:12px;">${change} <span class="note" style="display:inline; opacity:.7;">· ${esc(o.source)}</span></span></div>`;
}

// panelProfiles renders the Connection Profiles tab (RFC-0006 / issue #3): saved
// setups as cards with Activate / Export / Delete, a "save current setup as…"
// field, and Import. Data from GET /api/profiles (metadata only — never secrets).
function panelProfiles() {
  const save = card("SAVE CURRENT SETUP", `
    <div class="row">
      <label>Profile name</label>
      <input id="prof-name" placeholder="e.g. BM DMR duplex" maxlength="64" aria-label="New profile name">
    </div>
    <div style="display:flex; gap:12px; align-items:center; margin-top:6px;">
      <button type="button" id="prof-save"${profileBusy ? " disabled" : ""} style="padding:8px 18px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">SAVE PROFILE</button>
      <label class="prof-import" style="font-family:var(--mono); font-size:12px; cursor:pointer; text-decoration:underline;">
        Import from file…<input id="prof-import-file" type="file" accept=".json,application/json" style="display:none;">
      </label>
    </div>`);

  let listHTML;
  if (profiles === null) listHTML = note("Loading profiles…");
  else if (!profiles.length) listHTML = note("No profiles yet. Configure your modes and networks, then <b>Save current setup</b> to capture this configuration as a profile you can switch back to.");
  else listHTML = `<div class="stack">${profiles.map(profileCard).join("")}</div>`;

  const hint = note("Activating a profile writes its saved modes & networks and restarts the stack — the same as an Apply. Exported files have <b>passwords removed</b>; on import you re-enter any that are needed (or the target node keeps its own). Identity and calibration are never in a profile (<a href='https://github.com/KN4OQW/waypoint/blob/main/docs/rfcs/0006-connection-profiles.md'>RFC-0006</a>).");
  return `<div class="grid2">${save}${hint}</div>${listHTML}`;
}

function profileCard(p) {
  const badge = p.active
    ? `<span class="prof-badge" style="font-family:var(--mono); font-size:11px; color:var(--accent); border:1px solid var(--accent); border-radius:4px; padding:1px 6px;">ACTIVE</span>`
    : "";
  const fp = p.fingerprint || {};
  const fpText = (fp.rx_freq_hz || fp.tx_freq_hz)
    ? `RX ${mhz(fp.rx_freq_hz)} · TX ${mhz(fp.tx_freq_hz)} MHz`
    : "";
  const sens = (p.sensitive && p.sensitive.length)
    ? `<div class="note" style="margin:6px 0 0;">Needs re-entry on activate: ${p.sensitive.map(esc).join(", ")}</div>`
    : "";
  const dis = profileBusy ? " disabled" : "";
  return `<div class="card">
    <div class="card-head"><span class="sq"></span><span class="t">${esc(p.name)}</span> ${badge}</div>
    <div class="row"><label>Captured</label><span style="font-family:var(--mono); font-size:12px; opacity:.8;">${esc(p.updated_at || p.created_at || "—")}${fpText ? " · " + esc(fpText) : ""}</span></div>
    ${sens}
    <div style="display:flex; gap:10px; margin-top:8px; flex-wrap:wrap;">
      <button type="button" data-prof-activate="${esc(p.name)}"${dis} aria-label="Activate profile ${esc(p.name)}"${p.active ? " title='Already active'" : ""} style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">ACTIVATE</button>
      <button type="button" data-prof-export="${esc(p.name)}" aria-label="Export profile ${esc(p.name)}" style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:6px;">EXPORT</button>
      <button type="button" data-prof-delete="${esc(p.name)}"${dis} aria-label="Delete profile ${esc(p.name)}" style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:6px;">DELETE</button>
    </div>
  </div>`;
}

function panelPending(what) {
  return note(`<b>${esc(what)}</b> settings aren't wired yet — a later slice of the configuration store (<a href="https://github.com/KN4OQW/waypoint/issues/1">waypoint#1</a>).`);
}

// --- Network (host / OS) -------------------------------------------------
// The Network tab's first slice is read-only STATUS: the node's live host
// networking (interfaces, IPv4, DNS, Wi-Fi, NTP) parsed from nmcli/timedatectl and
// served at /api/network/status. The Wi-Fi / VLAN / static-IP EDIT surface (which
// writes the store and applies through the confirm-or-revert guard) lands in the
// next slice; the confirm countdown bar (showNetworkConfirmBar) is wired now so
// that surface has nothing left to build on the safety path.
// statRow renders a read-only status field. The input carries an aria-label (the
// visible <label> is not programmatically associated in this codebase's idiom), so
// each value is self-describing to a screen reader.
function statRow(label, value) {
  const v = value == null || value === "" ? "—" : value;
  return `<div class="row"><label>${esc(label)}</label><input value="${esc(v)}" readonly aria-label="${esc(label)}"></div>`;
}
function panelNetwork() {
  const live = netStatusSection();
  const editors = netEdit ? `${netHostCard()}${netEthCard()}${netWifiCard()}${netVlanCard()}` : note("Loading network configuration…");
  // The network Apply is SEPARATE from the radio Apply: it routes through the
  // confirm-or-revert guard (save → guarded apply → countdown). No direct-apply
  // escape hatch exists for host networking.
  const actions = `<div class="net-actions" style="display:flex; gap:12px; align-items:center; margin-top:6px;">
      <button type="button" id="net-apply"${netDirty ? "" : " disabled"} style="padding:8px 18px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">APPLY NETWORK</button>
      <span class="note" style="margin:0;">Applies through confirm-or-revert — you'll get a countdown to keep the change before it auto-reverts.</span>
    </div>`;
  const hint = note("Editing here writes the store and, on <b>Apply Network</b>, renders NetworkManager keyfiles behind a confirm-or-revert guard so a bad change can't strand the node. Waypoint only manages its own <code>waypoint-*</code> profiles; your hand-made connections are never touched.");
  return `<div class="grid2">${live}<div class="stack">${editors}</div></div>${actions}${hint}`;
}

// netStatusSection is the live, read-only host-network state (unchanged from the
// status-only slice): what the box is actually doing right now.
function netStatusSection() {
  if (!netStatus) return card("LIVE STATUS", note("Fetching live network status…"));
  const s = netStatus;
  const host = card("HOST (LIVE)", statRow("Hostname", s.hostname) + statRow("NTP", ntpText(s.ntp)) + (s.wifi ? statRow("Wi-Fi", `${s.wifi.ssid} · ${s.wifi.signal}%`) : ""));
  const devs = (s.devices || []).filter((d) => d.type === "ethernet" || d.type === "wifi");
  const devCards = devs.length ? devs.map(deviceCard).join("") : note("No live Ethernet or Wi-Fi interfaces reported.");
  return `${host}${devCards}`;
}
function ntpText(ntp) {
  if (!ntp) return "—";
  const state = ntp.enabled ? (ntp.synchronized ? "synchronized" : "enabled, not yet synced") : "disabled";
  return ntp.server ? `${state} · ${ntp.server}` : state;
}
function deviceCard(d) {
  const title = `${d.name} · ${d.type.toUpperCase()} (LIVE)`;
  const conn = d.connection ? d.connection + (d.managed ? " (waypoint)" : "") : "—";
  return card(title,
    statRow("State", d.state) +
    statRow("Profile", conn) +
    statRow("IPv4", d.ipv4 + (d.method ? ` (${d.method})` : "")) +
    statRow("Gateway", d.gateway) +
    statRow("DNS", (d.dns || []).join(", ")) +
    statRow("MAC", d.mac));
}

// --- Network editable config (goes through confirm-or-revert) ------------
// netEdit is the working copy of GET /api/network/config, kept separate from the
// radio `edit`/`dirty` state so a network change never rides the radio Apply (it
// must go through the guard). buildNetEdit normalizes the view into it.
function ipBlock(src) {
  return {
    method: (src && src.method) || "auto",
    address: (src && src.address) || "", prefix: (src && src.prefix) || "",
    gateway: (src && src.gateway) || "",
    dns: ((src && src.dns) || []).slice(),
    search_domains: ((src && src.search_domains) || []).slice(),
  };
}
function buildNetEdit(cfg) {
  cfg = cfg || {};
  netEdit = {
    host: { hostname: (cfg.host && cfg.host.hostname) || "", timezone: (cfg.host && cfg.host.timezone) || "" },
    ntp: { enabled: cfg.ntp ? cfg.ntp.enabled !== false : true, servers: ((cfg.ntp && cfg.ntp.servers) || []).slice() },
    connections: (cfg.connections || []).map((c) => ({
      name: c.name, type: c.type, interface: c.interface || "", autoconnect: c.autoconnect !== false,
      priority: c.priority || "", _managed: true, ipv4: ipBlock(c.ipv4),
      ssid: c.ssid || "", hidden: !!c.hidden, country: c.country || "", has_psk: !!c.has_psk, psk: "",
    })),
    vlans: (cfg.vlans || []).map((v) => ({ parent: v.parent || "", id: v.id || "", name: v.name || "", ipv4: ipBlock(v.ipv4) })),
  };
  // Ensure an Ethernet and a Wi-Fi slot exist so the cards always render; these
  // placeholders are only persisted once actually configured (netPersist).
  netEthConn(); netWifiConn();
  netDirty = false;
  netHostDirty = false;
}
function netConn(type, mk) {
  let c = netEdit.connections.find((x) => x.type === type);
  if (!c) { c = mk(); netEdit.connections.push(c); }
  return c;
}
function netBlankConn(over) {
  return Object.assign({ name: "", type: "", interface: "", autoconnect: true, priority: "", _managed: false,
    ipv4: { method: "auto", address: "", prefix: "", gateway: "", dns: [], search_domains: [] },
    ssid: "", hidden: false, country: "", has_psk: false, psk: "" }, over);
}
function netEthConn() { return netConn("ethernet", () => netBlankConn({ name: "eth0", type: "ethernet", interface: "eth0" })); }
function netWifiConn() { return netConn("wifi", () => netBlankConn({ name: "wifi", type: "wifi" })); }
function netMarkDirty() { netDirty = true; document.getElementById("net-apply") && (document.getElementById("net-apply").disabled = false); }

// netPersist decides whether a connection is written on Apply. A Wi-Fi profile
// needs an SSID; an Ethernet profile is persisted once it deviates from plain
// DHCP (static, a DNS/search override, or a priority) or is already managed — a
// pure-DHCP unmanaged Ethernet needs no waypoint-* profile at all (NM's default
// handles it), and switching a managed static profile back to DHCP with no
// overrides drops it, handing the interface back to NM's default DHCP.
function netPersist(c) {
  if (c.type === "wifi") return !!(c.ssid && c.ssid.trim());
  const ip = c.ipv4 || {};
  return c._managed || ip.method === "manual" || (ip.dns || []).length > 0 || (ip.search_domains || []).length > 0 || (c.priority && c.priority !== "0");
}
// netToPayload maps the flat edit shape to the store MODEL shape (nested wifi/
// ipv4), dropping view-only keys (has_psk, _managed) so the server's
// DisallowUnknownFields decode accepts it. A blank PSK is sent as "" and the
// server preserves the stored one (write-only secret).
function netToPayload(c) {
  const p = {
    name: c.name, type: c.type, interface: c.interface || "", autoconnect: !!c.autoconnect,
    priority: c.priority || "",
    ipv4: {
      method: c.ipv4.method || "auto", address: c.ipv4.address || "", prefix: c.ipv4.prefix || "",
      gateway: c.ipv4.gateway || "", dns: (c.ipv4.dns || []).slice(), search_domains: (c.ipv4.search_domains || []).slice(),
    },
  };
  if (c.type === "wifi") p.wifi = { ssid: c.ssid || "", psk: c.psk || "", hidden: !!c.hidden, country: (c.country || "").toUpperCase() };
  return p;
}
// vlanToPayload maps an edited VLAN to the store MODEL shape (id as a number).
function vlanToPayload(v) {
  return {
    parent: (v.parent || "").trim(), id: parseInt(v.id, 10) || 0, name: v.name || "",
    ipv4: {
      method: v.ipv4.method || "auto", address: v.ipv4.address || "", prefix: v.ipv4.prefix || "",
      gateway: v.ipv4.gateway || "", dns: (v.ipv4.dns || []).slice(), search_domains: (v.ipv4.search_domains || []).slice(),
    },
  };
}
// netIPv4Target resolves the ipv4 object an editor scope points at:
// "conn:<type>" → the ethernet/wifi connection; "vlan:<idx>" → that VLAN.
function netIPv4Target(scope) {
  const sep = scope.indexOf(":");
  const kind = scope.slice(0, sep), ref = scope.slice(sep + 1);
  if (kind === "vlan") return netEdit.vlans[+ref].ipv4;
  return netConnByType(ref).ipv4;
}
function netMarkHostDirty() { netHostDirty = true; const b = document.getElementById("host-apply"); if (b) b.disabled = false; }
function listToText(a) { return (a || []).join(", "); }
function textToList(s) { return String(s || "").split(/[\s,]+/).filter(Boolean); }

// ipv4Editor renders the shared DHCP/Static IPv4 sub-form. `scope` identifies the
// target ipv4 object for the event handlers ("conn:ethernet", "conn:wifi",
// "vlan:<idx>"); `label` names it for screen readers.
function ipv4Editor(ip, scope, label) {
  const isStatic = ip.method === "manual";
  const methodSel = row("IPv4 method",
    `<select data-netmethod="${esc(scope)}" aria-label="IPv4 method for ${esc(label)}">
       <option value="auto"${isStatic ? "" : " selected"}>DHCP (automatic)</option>
       <option value="manual"${isStatic ? " selected" : ""}>Static</option>
     </select>`);
  const staticFields = isStatic
    ? row("IP address", `<input data-netip="${esc(scope)}" data-ipkey="address" value="${esc(ip.address)}" placeholder="192.168.1.50" aria-label="IP address for ${esc(label)}">`) +
      row("Prefix (CIDR)", `<input data-netip="${esc(scope)}" data-ipkey="prefix" value="${esc(ip.prefix)}" placeholder="24" aria-label="Network prefix length for ${esc(label)}">`) +
      row("Gateway", `<input data-netip="${esc(scope)}" data-ipkey="gateway" value="${esc(ip.gateway)}" placeholder="192.168.1.1" aria-label="Default gateway for ${esc(label)}">`)
    : "";
  const dnsLabel = isStatic ? "DNS servers" : "DNS override (optional)";
  const dns = row(dnsLabel, `<input data-netdns="${esc(scope)}" value="${esc(listToText(ip.dns))}" placeholder="1.1.1.1, 8.8.8.8" aria-label="${esc(dnsLabel)} for ${esc(label)}">`) +
    (isStatic ? "" : note("With DHCP, listing DNS servers here <b>replaces</b> the ones the DHCP server hands out (ignore-auto-dns)."));
  const search = row("Search domains (optional)", `<input data-netsearch="${esc(scope)}" value="${esc(listToText(ip.search_domains))}" placeholder="lan, example.org" aria-label="DNS search domains for ${esc(label)}">`);
  return methodSel + staticFields + dns + search;
}
function netEthCard() {
  const c = netEthConn();
  return card("ETHERNET (waypoint-eth0)", ipv4Editor(c.ipv4, "conn:ethernet", "Ethernet"));
}
function netWifiCard() {
  const c = netWifiConn();
  const creds =
    row("SSID (network name)", `<input data-netwifi="wifi" data-wkey="ssid" value="${esc(c.ssid)}" placeholder="Your Wi-Fi name" aria-label="Wi-Fi SSID">`) +
    row("Passphrase", `<input data-netpsk="wifi" type="password" value="${esc(c.psk)}" placeholder="${c.has_psk ? "•••••• unchanged" : "Wi-Fi passphrase"}" aria-label="Wi-Fi passphrase">`) +
    switchRow("Hidden network", "nethidden", "wifi", c.hidden) +
    row("Regulatory country", `<input data-netwifi="wifi" data-wkey="country" value="${esc(c.country)}" maxlength="2" placeholder="US" aria-label="Regulatory country code">`);
  return card("WI-FI (waypoint-wifi)", creds) + netScanSection() + card("WI-FI IPv4", ipv4Editor(c.ipv4, "conn:wifi", "Wi-Fi"));
}

// switchRow renders an accessible on/off switch: a role="switch" pill with
// aria-checked, focusable and Enter/Space-operable (see the panels keydown handler).
// dataAttr is the data-* hook name; ref is the value passed to the handler.
function switchRow(label, dataAttr, ref, on) {
  return `<div class="toggle-row"><span class="name" id="sw-${esc(dataAttr)}-${esc(ref)}">${esc(label)}</span>` +
    `<span class="pill ${on ? "on" : "off"}" data-${esc(dataAttr)}="${esc(ref)}" role="switch" aria-checked="${on ? "true" : "false"}" aria-labelledby="sw-${esc(dataAttr)}-${esc(ref)}" tabindex="0" style="cursor:pointer;">${on ? "ON" : "OFF"}</span></div>`;
}

// netHostCard: hostname, timezone (searchable via a datalist), NTP enable + servers.
// These APPLY DIRECTLY (no guard — they can't strand the node), so the card has its
// own Apply button distinct from the guarded network apply.
function netHostCard() {
  const h = netEdit.host, n = netEdit.ntp;
  const tzOptions = (netTimezones || []).map((z) => `<option value="${esc(z)}"></option>`).join("");
  const liveTz = netStatus && netStatus.timezone ? ` <span class="note" style="margin:0">(now: ${esc(netStatus.timezone)})</span>` : "";
  const body =
    row("Hostname", `<input data-hostf="1" data-hkey="hostname" value="${esc(h.hostname)}" placeholder="${esc((netStatus && netStatus.hostname) || "waypoint")}" aria-label="Hostname">`) +
    row("Timezone", `<input list="tz-list" data-hostf="1" data-hkey="timezone" value="${esc(h.timezone)}" placeholder="Region/City" aria-label="Timezone (type to search)"><datalist id="tz-list">${tzOptions}</datalist>${liveTz}`) +
    switchRow("NTP time sync", "netntp", "1", n.enabled) +
    row("NTP servers (optional)", `<input data-ntpservers="1" value="${esc(listToText(n.servers))}" placeholder="pool.ntp.org, time.cloudflare.com" aria-label="NTP servers">`) +
    `<div style="margin-top:10px;"><button type="button" id="host-apply"${netHostDirty ? "" : " disabled"} style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">APPLY HOST SETTINGS</button> <span class="note" style="margin:0;">Applies immediately (hostname, timezone &amp; NTP can't strand the node).</span></div>`;
  return card("HOST · TIME · NTP", body);
}

// netVlanCard: the VLAN list. Each VLAN is a tagged interface on a parent, with its
// own IPv4 block. VLANs render NM type=vlan keyfiles and go through the CONFIRM-OR-
// REVERT guard (a bad VLAN can cut the uplink), so they save with "Apply Network".
function netVlanCard() {
  const vlans = netEdit.vlans || [];
  const blocks = vlans.map((v, i) => {
    const head = `<div class="toggle-row"><span class="name">VLAN ${esc(v.id || "?")}${v.name ? " · " + esc(v.name) : ""} <span class="note" style="margin:0">(waypoint-vlan${esc(v.id || "?")})</span></span>` +
      `<button type="button" class="pill off" data-vlandel="${i}" style="cursor:pointer;" aria-label="Remove VLAN ${esc(v.id || "")}">REMOVE</button></div>`;
    const fields =
      row("Parent interface", `<input data-vlanf="${i}" data-vkey="parent" value="${esc(v.parent)}" placeholder="eth0" aria-label="VLAN ${esc(v.id || "")} parent interface">`) +
      row("VLAN id (1–4094)", `<input data-vlanf="${i}" data-vkey="id" type="number" min="1" max="4094" value="${esc(v.id)}" placeholder="50" aria-label="VLAN id">`) +
      row("Label (optional)", `<input data-vlanf="${i}" data-vkey="name" value="${esc(v.name)}" placeholder="iot" aria-label="VLAN label">`) +
      ipv4Editor(v.ipv4, "vlan:" + i, "VLAN " + (v.id || (i + 1)));
    return `<div style="border-top:1px solid var(--line,rgba(128,128,128,0.25)); padding-top:8px; margin-top:8px;">${head}${fields}</div>`;
  }).join("");
  const empty = vlans.length ? "" : note("No VLANs. Add one to put a tagged interface on a parent device.");
  const add = `<div style="margin-top:10px;"><button type="button" id="vlan-add" style="padding:6px 14px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--accent); border:1px solid var(--accent); border-radius:6px;">+ ADD VLAN</button></div>`;
  return card("VLANs", empty + blocks + add);
}
function netScanSection() {
  const rows = (netScanResults || []).map((n) => {
    const lock = n.security ? "🔒" : "";
    const inuse = n.in_use ? ' <span style="color:var(--accent)">· connected</span>' : "";
    return `<div class="toggle-row"><span class="name">${esc(n.ssid)} ${lock} <span style="opacity:0.6">${n.signal}%</span>${inuse}</span>` +
      `<button type="button" class="pill off" data-netjoin="${esc(n.ssid)}" data-netsec="${esc(n.security)}" style="cursor:pointer;" aria-label="Join Wi-Fi network ${esc(n.ssid)}">JOIN</button></div>`;
  }).join("");
  const body = (netScanResults && netScanResults.length) ? rows : note("No networks found yet — press Rescan, or enter an SSID above for a hidden network.");
  const refresh = `<div style="margin-top:8px;"><button type="button" id="net-scan-refresh" style="padding:6px 14px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--accent); border:1px solid var(--accent); border-radius:6px;">RESCAN</button></div>`;
  return card("NEARBY NETWORKS", body + refresh);
}

// showNetworkConfirmBar renders the "Keep these settings?" countdown after a
// network apply. The rollback is enforced SERVER-SIDE on a timer (the node reverts
// even if this page never loads again); this bar is just the operator's chance to
// make the change permanent before the deadline. Confirm POSTs the token; letting
// it hit zero lets the server roll back on its own.
function showNetworkConfirmBar(deadlineISO, token) {
  const deadline = Date.parse(deadlineISO);
  if (isNaN(deadline)) return;
  clearInterval(netCountdown);
  hideNetworkConfirmBar();

  // Build the bar's DOM once. role="alert" announces the warning a single time;
  // the per-second countdown lives in an aria-hidden span so it is NOT re-announced
  // every tick (only the visible number changes). The message text is meaningful
  // without the exact seconds, so a screen-reader user still understands the stakes.
  const bar = el("div");
  bar.id = "net-confirm-bar";
  bar.setAttribute("role", "alert");
  bar.setAttribute("aria-live", "assertive");
  bar.style.cssText = "position:fixed; left:0; right:0; bottom:0; z-index:50; padding:13px 18px; display:flex; align-items:center; gap:16px; font-family:var(--mono); font-size:13px; background:rgba(255,107,107,0.10); border-top:1px solid var(--warn); color:var(--warn);";

  const msg = el("span");
  msg.innerHTML = `<b>Keep these network settings?</b> The node reverts automatically (in <span id="net-count" aria-hidden="true">…</span>) unless you confirm it is still reachable.`;
  bar.appendChild(msg);

  if (token) {
    const btn = el("button", "", "Keep settings");
    btn.type = "button";
    btn.setAttribute("aria-label", "Keep these network settings and cancel the automatic revert");
    btn.style.cssText = "margin-left:auto; padding:6px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--warn); color:#000; border:none; border-radius:6px;";
    btn.onclick = () => confirmNetwork(token);
    bar.appendChild(btn);
  }
  document.body.appendChild(bar);
  // Move focus to the confirm control so a keyboard user lands on the decision.
  const btn = bar.querySelector("button");
  if (btn) btn.focus();

  const count = bar.querySelector("#net-count");
  const tick = () => {
    const left = Math.max(0, Math.round((deadline - Date.now()) / 1000));
    if (count) count.textContent = left + "s";
    if (left <= 0) {
      clearInterval(netCountdown);
      netCountdown = null;
      // A meaningful state change — replacing the alert's content announces it once.
      bar.textContent = "Network change reverted — the confirm window elapsed, so the node rolled back to its previous settings.";
      setTimeout(hideNetworkConfirmBar, 4000);
      loadNetwork();
    }
  };
  tick();
  netCountdown = setInterval(tick, 1000);
}
function hideNetworkConfirmBar() {
  const bar = document.getElementById("net-confirm-bar");
  if (bar) bar.remove();
  clearInterval(netCountdown);
  netCountdown = null;
}
async function confirmNetwork(token) {
  if (!token) { banner("This browser doesn't hold the confirm token for the pending change — confirm from the tab that applied it, or let it revert.", "bad"); return; }
  try {
    const r = await fetch("/api/network/confirm", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }) });
    if (!r.ok) throw new Error((await r.text()).trim());
    sessionStorage.removeItem("wp-net-token");
    hideNetworkConfirmBar();
    banner("Network settings kept.", "ok");
    loadNetwork();
  } catch (err) {
    banner("Confirm failed: " + String(err.message || err), "bad");
  }
}

// The Gateways tab: the per-bridge-daemon cross-mode surface (YSF2DMR/DMR2YSF/
// YSF2NXDN/DMR2NXDN/NXDN2DMR) is retired in favour of the RFC-0003 bus
// architecture — a user creates a named bus and attaches modes to it, and traffic
// entering from any attached mode is converted and emitted to the others. The tab
// remains as a placeholder so the redesign has a home; the bridge store sections
// are kept dormant (disabling loses nothing — RFC-0001), so no data is lost.
// --- Buses (RFC-0003) -----------------------------------------------------
// The Gateways tab is the bus surface: a bus is a named hub that attached modes
// (DMR/YSF/NXDN) hear each other through. All validity (which modes may share a
// bus) comes from the server's one validator via /api/buses/validate — the JS
// never re-implements the converter matrix (RFC-0003 §2). Buses/attachments save
// and apply through the standard SetSection → render → apply path, so an enabled
// bus (re)starts waypoint-bus@<id>.service like any other target.

// The modes offered in the attach picker. DMR/YSF/NXDN attach today (reframe
// tier); D-Star/P25/M17 are offered so the validator can explain why they can't
// (transcode tier deferred), rather than hiding them.
const BUS_MODES = [
  { key: "dmr", label: "DMR" }, { key: "ysf", label: "YSF" }, { key: "nxdn", label: "NXDN" },
  { key: "dstar", label: "D-Star" }, { key: "p25", label: "P25" }, { key: "m17", label: "M17" },
];
const BUS_MODE_LABEL = Object.fromEntries(BUS_MODES.map((m) => [m.key, m.label]));

let attachPicker = null;   // { busId, loading?, opts:[{key,label,ok,reason}] } — open picker state
let busMigrateMsg = "";    // last migration result/warning line
let busBusy = {};          // bus name/id -> { winner, loser } while a losing source is held off
let busBusyTimers = {};    // bus name/id -> timeout id (busy is transient)

// attachFrom normalizes a stored attachment into the edit model, expanding the
// tg_map object into ordered editable rows (_tgrows).
function attachFrom(a) {
  return {
    bus_id: a.bus_id, mode: a.mode, credentials_ref: a.credentials_ref || "",
    slot: a.slot || "", default_tg: a.default_tg || "",
    target: a.target || "", wiresx_passthrough: !!a.wiresx_passthrough,
    id: a.id || "", tg: a.tg || "", default_id: a.default_id || "",
    _tgrows: Object.entries(a.tg_map || {}).map(([from, to]) => ({ from, to })),
  };
}

// cleanAttachment folds an edit attachment back into the store shape: _tgrows ->
// tg_map object (dropping blank rows), and only the fields meaningful for the
// mode. A bus holds NO secret — there is no password field to strip or preserve.
function cleanAttachment(a) {
  const out = { bus_id: a.bus_id, mode: a.mode, credentials_ref: a.credentials_ref || "" };
  if (a.mode === "dmr") {
    out.slot = a.slot || "";
    out.default_tg = a.default_tg || "";
    const map = {};
    (a._tgrows || []).forEach((r) => { if (String(r.from).trim() && String(r.to).trim()) map[String(r.from).trim()] = String(r.to).trim(); });
    if (Object.keys(map).length) out.tg_map = map;
  } else if (a.mode === "ysf") {
    out.target = a.target || "";
    out.wiresx_passthrough = !!a.wiresx_passthrough;
  } else if (a.mode === "nxdn") {
    out.id = a.id || ""; out.tg = a.tg || ""; out.default_id = a.default_id || "";
  }
  return out;
}

function panelGateways() {
  const buses = edit.buses || [];
  const migrate = card("MIGRATE FROM RETIRED BRIDGES",
    note("The old per-mode bridges (YSF2DMR, DMR2YSF, YSF2NXDN, DMR2NXDN, NXDN2DMR) are retired. Migration seeds a bus from whatever you had configured — your saved bridge settings are <b>preserved either way</b>, migration only <b>adds</b> a bus, it never deletes anything.") +
    `<div class="row"><button type="button" class="btn accent" id="bus-migrate">Migrate bridges → bus</button></div>` +
    (busMigrateMsg ? note(esc(busMigrateMsg)) : ""));
  const list = buses.length
    ? buses.map(busCard).join("")
    : note("No buses yet. A <b>bus</b> lets its attached modes hear each other (DMR ⇄ YSF ⇄ NXDN), with IDs/talkgroups translated per side. Create one, then attach two or more modes.");
  const create = `<div class="row"><button type="button" class="btn" id="bus-create">＋ Create bus</button></div>`;
  return `<div class="stack">${peersCard()}${migrate}${list}${create}</div>`;
}

function busCard(bus) {
  const atts = (edit.attachments || []).filter((a) => a.bus_id === bus.id);
  const remotes = (edit.remote_attachments || []).filter((r) => r.bus_id === bus.id);
  const busy = busBusy[bus.name] || busBusy[bus.id];
  const busyBadge = busy
    ? `<span class="pill busy" title="Another source is talking; ${esc(busy.loser)} traffic is held off">busy: via ${esc(busy.winner)}${busy.node ? " @ " + esc(busy.node) : ""}</span>` : "";
  const enPill = `<button type="button" class="pill ${bus.enabled ? "on" : "off"}" data-busen="${esc(bus.id)}" aria-pressed="${bus.enabled}" aria-label="Bus enabled">${bus.enabled ? "ENABLED" : "DISABLED"}</button>`;
  const del = (atts.length === 0 && remotes.length === 0) ? `<button type="button" class="btn danger" data-busdel="${esc(bus.id)}">Delete</button>` : "";
  const head = `<div class="card-head"><span class="sq"></span><span class="t">${esc(bus.name || bus.id)}</span>${busyBadge}<span class="bus-actions">${enPill}${del}</span></div>`;
  const nameRow = row("Name", `<input data-busname="${esc(bus.id)}" value="${esc(bus.name)}" placeholder="e.g. Local Bus A">`);
  // Owner-offline state on a member (RFC-0016 §4), self-clearing (no latch).
  const down = busDown[bus.name] || busDown[bus.id];
  const downNote = down ? `<div class="note bus-down"><b>Bus ${esc(bus.name || bus.id)} down</b> — owner ${esc(down)} offline</div>` : "";
  const disableNote = bus.enabled ? "" : note("Disabled — its attachments are kept and return when you re-enable it.");
  const total = atts.length + remotes.length;
  const lowNote = (bus.enabled && total < 2) ? note("A bus needs at least two attachments to hub traffic; add another mode before applying.") : "";
  const attHTML = atts.map((a) => attachmentBlock(a, edit.attachments.indexOf(a))).join("");
  const remoteHTML = remotes.map(remoteAttachmentBlock).join("");
  return `<div class="card bus-card">${head}${downNote}${nameRow}${disableNote}${lowNote}${attHTML}${remoteHTML}${attachPickerHTML(bus.id)}</div>`;
}

// remoteAttachmentBlock renders a via-peer edge: mode @ peer, DORMANT when the peer
// is not paired (RFC-0016 — the edge renders nothing until re-paired), with detach.
function remoteAttachmentBlock(r) {
  const peer = (edit.peers || []).find((p) => p.id === r.peer_id);
  const pname = peer ? (peer.name || peer.id) : r.peer_id;
  const dormant = !peer || peer.state !== "paired";
  const badge = dormant
    ? `<span class="pill off" title="peer not paired — this edge is dormant until re-paired">DORMANT</span>`
    : `<span class="pill on">VIA PEER</span>`;
  const key = `${r.bus_id}|${r.peer_id}|${r.mode}`;
  return `<div class="attach remote-attach"><div class="toggle-row"><span class="name">${esc(BUS_MODE_LABEL[r.mode] || r.mode)} @ ${esc(pname)} ${badge}</span><button type="button" class="btn" data-remotedel="${esc(key)}">Detach</button></div></div>`;
}

function attachmentBlock(a, idx) {
  const headRow = `<div class="toggle-row"><span class="name">${esc(BUS_MODE_LABEL[a.mode] || a.mode)} attachment</span><button type="button" class="btn" data-attachdel="${idx}">Detach</button></div>`;
  return `<div class="attach">${headRow}${attachParams(a, idx)}</div>`;
}

function attachParams(a, idx) {
  if (a.mode === "dmr") {
    const slot = a.slot || "2";
    const slotSel = row("Slot", `<select data-attach="${idx}" data-akey="slot"><option value="1"${slot === "1" ? " selected" : ""}>1</option><option value="2"${slot === "2" ? " selected" : ""}>2</option></select>`);
    const nets = edit.networks || [];
    const creds = row("Credentials (DMR network)",
      `<select data-attach="${idx}" data-akey="credentials_ref"><option value="">(none — rides local DMRGateway)</option>${nets.map((n) => `<option value="${esc(n.name)}"${a.credentials_ref === n.name ? " selected" : ""}>${esc(n.name)}</option>`).join("")}</select>`);
    return slotSel + attField(idx, "default_tg", "Default TG", "e.g. 91") + creds + tgMapEditor(a, idx);
  }
  if (a.mode === "ysf") {
    const opts = ysfRefs.map((r) => `<option value="${esc(r.name)}">${esc([r.country, r.description].filter(Boolean).join(" · "))}</option>`).join("");
    const target = row("Reflector / DG-ID", `<input data-attach="${idx}" data-akey="target" list="bus-ysf-refs" value="${esc(a.target || "")}" placeholder="e.g. FCS00290 or a YSF reflector"><datalist id="bus-ysf-refs">${opts}</datalist>`);
    const wx = `<div class="toggle-row"><span class="name">Wires-X passthrough</span><button type="button" class="pill ${a.wiresx_passthrough ? "on" : "off"}" data-attachbool="${idx}" data-abkey="wiresx_passthrough" aria-pressed="${a.wiresx_passthrough}" aria-label="Wires-X passthrough">${a.wiresx_passthrough ? "ON" : "OFF"}</button></div>`;
    return target + wx;
  }
  if (a.mode === "nxdn") {
    return attField(idx, "id", "NXDN ID", "network id") + attField(idx, "tg", "TG", "talkgroup") + attField(idx, "default_id", "Default ID", "");
  }
  return note("This mode cannot attach in the committed reframe tier.");
}

function attField(idx, key, label, ph) {
  const v = (edit.attachments[idx] || {})[key] || "";
  return row(label, `<input data-attach="${idx}" data-akey="${esc(key)}" value="${esc(v)}" placeholder="${esc(ph || "")}">`);
}

function tgMapEditor(a, idx) {
  const rows = (a._tgrows || []).map((r, ri) =>
    `<div class="row tgmap-row"><input data-tgmap="${idx}" data-tgi="${ri}" data-tgk="from" value="${esc(r.from)}" placeholder="source TG"><span class="arrow">→</span><input data-tgmap="${idx}" data-tgi="${ri}" data-tgk="to" value="${esc(r.to)}" placeholder="DMR TG"><button type="button" class="btn" data-tgdel="${idx}" data-tgi="${ri}" aria-label="Remove mapping">✕</button></div>`).join("");
  return `<div class="note">TG map — rewrite a source-side talkgroup to a DMR TG (optional)</div>${rows}<div class="row"><button type="button" class="btn" data-tgadd="${idx}">＋ Add mapping</button></div>`;
}

function attachPickerHTML(busId) {
  if (!attachPicker || attachPicker.busId !== busId) {
    return `<div class="row"><button type="button" class="btn" data-attachopen="${esc(busId)}">＋ Attach mode</button></div>`;
  }
  if (attachPicker.loading) return note("Checking which modes can attach…");
  const btns = (attachPicker.opts || []).map((o) =>
    o.ok
      ? `<button type="button" class="btn attach-ok" data-attachpick="${esc(o.key)}">${esc(o.label)}</button>`
      : `<button type="button" class="btn attach-no" disabled title="${esc(o.reason)}">${esc(o.label)} — ${esc(o.reason)}</button>`).join("");
  // "Via peer" source (RFC-0016): pick a paired peer, then a mode — greyed with the
  // peering-specific reasons from the server validator (never re-derived in JS).
  const paired = (edit.peers || []).filter((p) => p.state === "paired");
  let peerSection = "";
  if (paired.length) {
    const peerBtns = paired.map((p) => `<button type="button" class="btn${attachPicker.remote && attachPicker.remote.peerId === p.id ? " attach-ok" : ""}" data-attachpeer="${esc(p.id)}">via ${esc(p.name || p.id)}</button>`).join("");
    peerSection = `<div class="note">Or attach a mode from a paired peer:</div><div class="picker-row">${peerBtns}</div>`;
    const rp = attachPicker.remote;
    if (rp) {
      const rbtns = rp.loading ? "Checking…" : (rp.opts || []).map((o) =>
        o.ok
          ? `<button type="button" class="btn attach-ok" data-attachrpick="${esc(rp.peerId)}|${esc(o.key)}">${esc(o.label)}</button>`
          : `<button type="button" class="btn attach-no" disabled title="${esc(o.reason)}">${esc(o.label)} — ${esc(o.reason)}</button>`).join("");
      peerSection += `<div class="note">via ${esc(peerName(rp.peerId))}:</div><div class="picker-row">${rbtns}</div>`;
    }
  }
  return `<div class="attach-picker"><div class="note">Attach a local mode (greyed-out modes can't, reason shown):</div><div class="picker-row">${btns}</div>${peerSection}<div class="row"><button type="button" class="btn" data-attachcancel="1">Cancel</button></div></div>`;
}

function peerName(id) { const p = (edit.peers || []).find((x) => x.id === id); return p ? (p.name || p.id) : id; }

// openRemoteAttachPicker validates each of the peer's modes as a remote attachment
// via the server validator (peering-specific reasons, the union mode-set, and the
// node cap all come back verbatim) — never decided in JS.
async function openRemoteAttachPicker(busId, peerId) {
  attachPicker.remote = { peerId, loading: true };
  renderPanel();
  const existing = (edit.remote_attachments || []).map((r) => ({ bus_id: r.bus_id, peer_id: r.peer_id, mode: r.mode }));
  const have = new Set(existing.filter((r) => r.bus_id === busId && r.peer_id === peerId).map((r) => r.mode));
  const cands = BUS_MODES.filter((m) => !have.has(m.key));
  const opts = await Promise.all(cands.map(async (m) => {
    const remote_attachments = existing.concat([{ bus_id: busId, peer_id: peerId, mode: m.key }]);
    try {
      const r = await fetch("/api/buses/validate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ buses: edit.buses || [], attachments: (edit.attachments || []).map(cleanAttachment), remote_attachments }) });
      const j = await r.json();
      return { key: m.key, label: m.label, ok: !!j.ok, reason: (j.reason || "").replace(/^bus "[^"]*":\s*/, "") };
    } catch (e) { return { key: m.key, label: m.label, ok: false, reason: "validation unavailable" }; }
  }));
  if (attachPicker && attachPicker.busId === busId && attachPicker.remote && attachPicker.remote.peerId === peerId) {
    attachPicker.remote = { peerId, opts };
    renderPanel();
  }
}

function attachRemote(busId, peerId, mode) {
  (edit.remote_attachments = edit.remote_attachments || []).push({ bus_id: busId, peer_id: peerId, mode, target: "", default_tg: "", slot: "", tg: "", id: "", default_id: "" });
  attachPicker = null;
  dirty.add("remote_attachments");
  renderPanel(); refreshActions();
}

function detachRemote(key) {
  const [bus_id, peer_id, mode] = key.split("|");
  edit.remote_attachments = (edit.remote_attachments || []).filter((r) => !(r.bus_id === bus_id && r.peer_id === peer_id && r.mode === mode));
  dirty.add("remote_attachments");
  renderPanel(); refreshActions();
}

// newBusId mints a short, unique, stable id (bus-N) — the id drives the rendered
// file name and unit (waypoint-bus@<id>.service), so it must not collide.
function newBusId() {
  const ids = new Set((edit.buses || []).map((b) => b.id));
  let n = 1;
  while (ids.has("bus-" + n)) n++;
  return "bus-" + n;
}

function createBus() {
  const id = newBusId();
  (edit.buses = edit.buses || []).push({ id, name: "New Bus " + id.slice(4), enabled: true });
  dirty.add("buses");
  attachPicker = null;
  renderPanel(); refreshActions();
}

function toggleBus(id) {
  const b = (edit.buses || []).find((x) => x.id === id);
  if (!b) return;
  b.enabled = !b.enabled;
  dirty.add("buses");
  renderPanel(); refreshActions();
}

function deleteBus(id) {
  const atts = (edit.attachments || []).filter((a) => a.bus_id === id);
  if (atts.length) return; // guarded in UI; delete only an empty bus
  edit.buses = (edit.buses || []).filter((b) => b.id !== id);
  dirty.add("buses");
  renderPanel(); refreshActions();
}

function detachMode(idx) {
  if (!edit.attachments || !edit.attachments[idx]) return;
  edit.attachments.splice(idx, 1);
  dirty.add("attachments");
  renderPanel(); refreshActions();
}

// openAttachPicker asks the server validator whether each not-yet-attached mode
// could join this bus, so the picker greys out the impossible ones with the exact
// reason (RFC-0003 §2). It never decides validity in JS.
async function openAttachPicker(busId) {
  attachPicker = { busId, loading: true };
  renderPanel();
  const attached = new Set((edit.attachments || []).filter((a) => a.bus_id === busId).map((a) => a.mode));
  const cands = BUS_MODES.filter((m) => !attached.has(m.key));
  const opts = await Promise.all(cands.map(async (m) => {
    const attachments = (edit.attachments || []).map(cleanAttachment).concat([{ bus_id: busId, mode: m.key }]);
    try {
      const r = await fetch("/api/buses/validate", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ buses: edit.buses || [], attachments }) });
      const j = await r.json();
      return { key: m.key, label: m.label, ok: !!j.ok, reason: (j.reason || "").replace(/^bus "[^"]*":\s*/, "") };
    } catch (e) {
      return { key: m.key, label: m.label, ok: false, reason: "validation unavailable" };
    }
  }));
  // Only apply if the picker is still open for this bus (user may have cancelled).
  if (attachPicker && attachPicker.busId === busId) { attachPicker = { busId, opts }; renderPanel(); }
}

function attachMode(busId, mode) {
  const a = { bus_id: busId, mode, credentials_ref: "", _tgrows: [] };
  if (mode === "dmr") { a.slot = "2"; a.default_tg = ""; }
  else if (mode === "ysf") { a.target = ""; a.wiresx_passthrough = false; }
  else if (mode === "nxdn") { a.id = ""; a.tg = ""; a.default_id = ""; }
  (edit.attachments = edit.attachments || []).push(a);
  attachPicker = null;
  dirty.add("attachments");
  renderPanel(); refreshActions();
}

// runMigration invokes the server-side bridge→bus seeding, which persists the
// result itself; we reload to show it and report the warnings verbatim. The
// migrated bus still needs an Apply to start — the copy says so.
async function runMigration() {
  busMigrateMsg = "Migrating…";
  renderPanel();
  try {
    const r = await fetch("/api/buses/migrate", { method: "POST" });
    const j = await r.json();
    if (!r.ok) { busMigrateMsg = "Migration failed: " + (j.reason || (await r.text())); renderPanel(); return; }
    const warns = (j.warnings || []).join("  ");
    if (j.ok) {
      await load();
      busMigrateMsg = `Migrated ${j.buses} bus (${j.attachments} attachments). Review it below, then Apply to start it. ${warns}`.trim();
    } else {
      busMigrateMsg = warns || "Nothing to migrate.";
    }
    renderPanel();
  } catch (e) {
    busMigrateMsg = "Migration error: " + String(e);
    renderPanel();
  }
}

// initBusEvents surfaces the daemon's transient bus_busy events on the bus cards
// (RFC-0003 §5 / issue #65 acceptance #5). The event carries the bus name in
// `network`, the winner in `source`, the loser in `mode`.
function initBusEvents() {
  try {
    const es = new EventSource("/api/events");
    es.onmessage = (m) => {
      let e; try { e = JSON.parse(m.data); } catch (_) { return; }
      const key = e.network || "";
      const draw = () => { if (state.tab === "gateways") renderPanel(); };
      if (e.type === "bus_busy") {
        if (busBusyTimers[key]) clearTimeout(busBusyTimers[key]);
        // e.node carries the origin node when the source is a remote peer (RFC-0016)
        busBusy[key] = { winner: e.source || "", loser: e.mode || "", node: e.node || "" };
        busBusyTimers[key] = setTimeout(() => { delete busBusy[key]; draw(); }, 2500);
        clearBusDown(key); // any traffic implies the owner is up — self-clear (no latch)
        draw();
      } else if (e.type === "bus_voice_start" || e.type === "bus_up") {
        clearBusDown(key); draw(); // recovery clears the owner-offline state
      } else if (e.type === "bus_down") {
        if (busDownTimers[key]) clearTimeout(busDownTimers[key]);
        busDown[key] = e.source || "the owner"; // e.source = the owner node
        // failsafe self-clear so a missed recovery event never latches forever
        busDownTimers[key] = setTimeout(() => { delete busDown[key]; draw(); }, 30000);
        draw();
      }
    };
    es.onerror = () => {}; // EventSource auto-reconnects
  } catch (e) { /* no live surfacing if the stream is unavailable */ }
}

// --- Bus LAN peering (RFC-0016) ------------------------------------------
let peering = { discovered: null, pending: [], busy: false, msg: "" };
let busDown = {};        // bus name/id -> owner node label (owner-offline; self-clears, no latch)
let busDownTimers = {};
function clearBusDown(key) { if (busDownTimers[key]) { clearTimeout(busDownTimers[key]); delete busDownTimers[key]; } delete busDown[key]; }

// peersCard is the LAN peers surface on the Gateways tab: mDNS discovery + manual
// add, the paired/revoked list with fingerprints, and revoke. The active pairing
// (short code + fingerprint) is a prominent modal (peerModal).
function peersCard() {
  const peers = edit.peers || [];
  const paired = peers.filter((p) => p.state === "paired");
  const other = peers.filter((p) => p.state !== "paired");

  const pairedRows = paired.length
    ? paired.map((p) => `<div class="toggle-row peer-row"><span class="name">${esc(p.name || p.id)}<span class="peer-fp" title="certificate fingerprint">${esc(shortFp(p.fingerprint))}</span></span><span class="bus-actions"><span class="pill on">PAIRED</span><button type="button" class="btn danger" data-peerrevoke="${esc(p.id)}" data-peername="${esc(p.name || p.id)}">Revoke</button></span></div>`).join("")
    : note("No paired peers yet. Discover a node or add one by host:port, then pair.");
  const otherRows = other.map((p) => `<div class="toggle-row peer-row muted"><span class="name">${esc(p.name || p.id)}<span class="peer-fp">${esc(shortFp(p.fingerprint))}</span></span><span class="pill off">${esc((p.state || "").toUpperCase())}</span></div>`).join("");

  let disc = "";
  if (peering.discovered !== null) {
    disc = peering.discovered.length
      ? peering.discovered.map((d) => `<div class="row peer-disc"><label>${esc(d.instance || d.host)}<span class="peer-fp">${esc(d.host)}:${esc(String(d.port))}</span></label><button type="button" class="btn accent" data-peerpair="${esc(d.host)}:${esc(String(d.port))}">Pair</button></div>`).join("")
      : note("No peers found on the LAN. Add one by host:port below (mDNS may be filtered on your network).");
  }
  const discBtn = `<button type="button" class="btn" id="peer-discover"${peering.busy ? " disabled" : ""}>${peering.busy ? "Scanning…" : "Discover peers (mDNS)"}</button>`;
  const manual = `<div class="row"><input id="peer-manual" placeholder="host:port — e.g. 10.0.0.20:42501" aria-label="peer host and port"><button type="button" class="btn" id="peer-pair-manual">Pair</button></div>`;

  return card("LAN PEERS (RFC-0016)",
    note("Pair Waypoint nodes on your LAN so a bus can span them. Pairing shows a short code on <b>both</b> screens — enter it to confirm; the certificate fingerprint is shown for out-of-band verification.") +
    `<div class="row">${discBtn}</div>` + disc + manual +
    (peering.msg ? note(esc(peering.msg)) : "") +
    `<div class="note">Paired peers</div>` + pairedRows + otherRows);
}

function shortFp(fp) {
  if (!fp) return "";
  // show the first and last groups so it fits a phone but stays verifiable
  const g = fp.split(":");
  return g.length > 6 ? `${g.slice(0, 4).join(":")}…${g.slice(-2).join(":")}` : fp;
}

// renderPeerModal draws the active pairing (if any) as a prominent, phone-readable
// overlay: the short code big + copyable, the peer fingerprint alongside, and the
// confirm/cancel actions — shown on BOTH ends (each end learns of the session via
// /api/peering/pending). The initiator sees the code; the responder enters it.
function renderPeerModal() {
  const root = document.getElementById("peer-modal");
  if (!root) return;
  const s = (peering.pending || [])[0];
  if (!s) { root.hidden = true; root.innerHTML = ""; return; }
  root.hidden = false;
  const peer = s.peer_name || s.peer_node || "the other node";
  const fpRow = s.fingerprint
    ? `<div class="pair-fp">fingerprint <span class="mono">${esc(s.fingerprint)}</span></div>`
    : `<div class="pair-fp muted">exchanging certificate…</div>`;
  let body;
  if (s.role === "initiator") {
    body = `<p>Enter this code on <b>${esc(peer)}</b>'s LAN Peers screen, then confirm here.</p>
      <div class="pair-code"><span class="mono" id="pair-code-val">${esc(s.code || "")}</span><button type="button" class="btn" data-paircopy="${esc(s.code || "")}" aria-label="copy code">Copy</button></div>
      ${fpRow}
      <div class="row pair-actions"><button type="button" class="btn accent" data-pairconfirm="${esc(s.sid)}">Confirm pairing</button><button type="button" class="btn" data-paircancel="${esc(s.sid)}">Cancel</button></div>`;
  } else {
    body = `<p>Incoming pairing from <b>${esc(peer)}</b>. Enter the code shown on <b>${esc(peer)}</b>:</p>
      <div class="pair-code"><input class="mono pair-input" id="pair-code-input" inputmode="numeric" maxlength="6" placeholder="000000" aria-label="pairing code"></div>
      ${fpRow}
      <div class="row pair-actions"><button type="button" class="btn accent" data-pairenter="${esc(s.sid)}">Confirm</button><button type="button" class="btn" data-paircancel="${esc(s.sid)}">Cancel</button></div>`;
  }
  root.innerHTML = `<div class="pair-backdrop"><div class="pair-modal card"><div class="card-head"><span class="sq"></span><span class="t">PAIRING</span></div>${body}</div></div>`;
}

async function peeringGet(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error(await r.text());
  return r.json();
}
async function peeringPost(path, body) {
  const r = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined });
  if (!r.ok) throw new Error((await r.text()).trim());
  return r.status === 204 ? null : r.json();
}

async function loadPending() {
  try { peering.pending = await peeringGet("/api/peering/pending") || []; }
  catch (e) { peering.pending = []; }
  renderPeerModal();
}

async function discoverPeers() {
  peering.busy = true; peering.msg = ""; renderPanel();
  try { peering.discovered = await peeringGet("/api/peering/discover") || []; }
  catch (e) { peering.msg = "Discovery unavailable (mDNS off?). Add a peer by host:port."; peering.discovered = []; }
  peering.busy = false; renderPanel();
}

async function pairWith(addr) {
  peering.msg = "";
  try {
    await peeringPost("/api/peering/initiate", { addr });
    await loadPending();
  } catch (e) { peering.msg = "Could not reach " + addr + ": " + String(e.message || e); renderPanel(); }
}

async function confirmPair(sid, code) {
  try {
    await peeringPost("/api/peering/confirm", { sid, code: code || "" });
    await loadPending();
    await load(); // refresh the paired list from the store
  } catch (e) { peering.msg = "Pairing failed: " + String(e.message || e); renderPeerModal(); renderPanel(); }
}

async function cancelPair(sid) {
  try { await peeringPost("/api/peering/cancel", { sid }); } catch (e) {}
  await loadPending();
}

async function revokePeer(peerId, peerName) {
  const buses = remoteBusesFor(peerId);
  const consequence = buses.length
    ? `Remote attachments on ${buses.join(", ")} will stop rendering when you Apply.`
    : "This node will refuse connections from that peer immediately.";
  if (!confirm(`Revoke pairing with ${peerName}?\n\n${consequence}\n\nRe-pairing later mints fresh keys.`)) return;
  try {
    await peeringPost("/api/peering/revoke", { peer_id: peerId });
    await load();
  } catch (e) { peering.msg = "Revoke failed: " + String(e.message || e); renderPanel(); }
}

// remoteBusesFor names the buses a peer contributes a mode to (for the revoke
// consequence copy).
function remoteBusesFor(peerId) {
  const seen = new Set();
  (edit.remote_attachments || []).filter((r) => r.peer_id === peerId).forEach((r) => {
    const b = (edit.buses || []).find((x) => x.id === r.bus_id);
    seen.add("Bus " + (b ? (b.name || b.id) : r.bus_id));
  });
  return [...seen];
}

// startPeeringPoll keeps the pending-pairing modal live on both ends (a responder
// learns of an incoming request this way) while the operator is on the tab.
let peeringPollTimer = null;
function startPeeringPoll() {
  if (peeringPollTimer) return;
  peeringPollTimer = setInterval(() => { if (state.tab === "gateways") loadPending(); }, 2000);
  loadPending();
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
  // Mode params render into MMDVM-Host's [System Fusion] (self_only, low_deviation,
  // remote_gateway, tx_hang, mode_hang) — the "ysf" store section, split from the
  // "ysfgw" gateway section like p25/p25gw and nxdn/nxdngw.
  const behaviour = card("BEHAVIOUR",
    toggleRow("ysf", "self_only", "Self only (accept only my callsign)") +
    toggleRow("ysf", "low_deviation", "Low deviation (narrow-band C4FM)") +
    toggleRow("ysf", "remote_gateway", "Remote gateway (advanced — leave off for local control)") +
    toggleRow("ysfgw", "wiresx_passthrough", "Wires-X passthrough (advanced — leave off for local control)") +
    toggleRow("ysfgw", "revert", "Revert to startup on inactivity"));
  const timers = card("HANG TIMERS",
    input("ysf", "tx_hang", { label: "TX hang", unit: "sec" }) +
    input("ysf", "mode_hang", { label: "Mode hang", unit: "sec" }));
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
  return `<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}${networks}${dgid}</div></div>${hint}`;
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

// panelStation is the Station Settings tab: node-wide operating policy that isn't
// a mode or a network. Today it holds event-history retention (RFC-0004); the
// callsign-beacon feature will land beside it here as its own card/section.
function panelStation() {
  const h = edit.history || (edit.history = { retention_days: 7 });
  const days = h.retention_days ?? 7;
  const retention = card("EVENT HISTORY",
    row("Retention window",
      `<div class="unit"><input data-sec="history" data-key="retention_days" data-kind="int" inputmode="numeric" value="${esc(days)}"><span class="u">days</span></div>`) +
    note("How long this node keeps its persistent last-heard and event log (stored on-device). <b>0 keeps history forever.</b> Older events are pruned nightly; a longer window uses more SD-card space."));
  const beacon = card("CALLSIGN BEACON",
    note("Automatic callsign identification will be configured here. <span style=\"color:var(--muted)\">Not yet available.</span>"));
  return `<div class="grid2">${retention}${beacon}</div>`;
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
    case "profiles":     box.innerHTML = panelProfiles(); break;
    case "station":      box.innerHTML = panelStation(); break;
    case "brandmeister": box.innerHTML = panelBrandmeister(); break;
    case "expert":       box.innerHTML = panelExpert(c, state.health); break;
    case "gateways":     box.innerHTML = panelGateways(); break;
    case "network":      box.innerHTML = panelNetwork(); break;
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
        : sec === "attachments" ? (edit.attachments || []).map(cleanAttachment)
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
  // The Network tab shows live system state, fetched on demand (not part of the
  // store config load).
  if (id === "network") loadNetwork();
  // The Expert tab's override view is fetched on demand (read-only, RFC-0005),
  // re-fetched each open so it reflects the current store render.
  if (id === "expert") loadOverrides();
  // Connection profiles are fetched on demand (RFC-0006), refreshed each open so
  // the ACTIVE badge reflects the live store.
  if (id === "profiles") loadProfiles();
}

function renderThemes() {
  const box = document.getElementById("swatches");
  box.innerHTML = ""; // re-render replaces the swatches instead of appending
  const cur = localStorage.getItem("wp-theme") || "phosphor";
  applyTheme(cur);
  const mode = currentMode();
  applyMode(mode);
  // Dark/Light toggle first (RFC-0009), then the accent swatches.
  const toggle = el("button", "swatch mode-toggle" + (mode === "light" ? " light" : ""));
  toggle.type = "button";
  toggle.title = mode === "light" ? "Switch to dark" : "Switch to light";
  toggle.setAttribute("aria-label", "Toggle light mode");
  toggle.setAttribute("aria-pressed", String(mode === "light"));
  toggle.textContent = mode === "light" ? "☀ Light" : "☾ Dark";
  toggle.onclick = () => {
    const next = currentMode() === "light" ? "dark" : "light";
    localStorage.setItem("wp-mode", next);
    applyMode(next);
    renderThemes();
  };
  box.appendChild(toggle);
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
// Dark is the default; "light" is a mode composing with the accent theme (RFC-0009).
function currentMode() {
  const m = localStorage.getItem("wp-mode");
  if (m) return m;
  return (window.matchMedia && matchMedia("(prefers-color-scheme: light)").matches) ? "light" : "dark";
}
function applyMode(mode) {
  if (mode === "light") document.documentElement.setAttribute("data-mode", "light");
  else document.documentElement.removeAttribute("data-mode");
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
  try {
    dmrTGs = await fetch("/api/dmr/talkgroups").then((r) => r.json()) || [];
    if (state.tab === "dmr") renderPanel();
  } catch { /* offline — the TG picker still accepts a typed number */ }
  // Host-network status is live system state, fetched separately from the store
  // config. Refresh whenever the Network tab is showing.
  if (state.tab === "network") loadNetwork();
  // Resume a confirm-or-revert countdown if a network apply is mid-window (e.g. the
  // page reloaded after applying). The deadline comes from the server; the token is
  // held in sessionStorage by the tab that applied.
  try {
    const nc = await fetch("/api/network/config").then((r) => r.json());
    if (nc && nc.pending_confirm) showNetworkConfirmBar(nc.pending_confirm.deadline, sessionStorage.getItem("wp-net-token"));
  } catch { /* no store / offline */ }
}

// loadNetwork fetches the live status, the editable config, and a Wi-Fi scan, then
// re-renders the Network tab. Config is rebuilt into netEdit only when there are no
// unsaved edits, so a background refresh never clobbers what the operator is typing.
async function loadNetwork() {
  const [st, cfg, scan] = await Promise.allSettled([
    fetch("/api/network/status").then((r) => r.json()),
    fetch("/api/network/config").then((r) => r.json()),
    fetch("/api/network/wifi/scan").then((r) => r.json()),
  ]);
  netStatus = st.status === "fulfilled" ? st.value : null;
  netScanResults = scan.status === "fulfilled" && Array.isArray(scan.value) ? scan.value : [];
  // Only rebuild netEdit when there are no unsaved edits of EITHER kind, so a
  // background refresh never clobbers what the operator is typing.
  if (cfg.status === "fulfilled" && !netDirty && !netHostDirty) buildNetEdit(cfg.value);
  else if (!netEdit) buildNetEdit({});
  if (!netTimezones.length) {
    try { netTimezones = (await fetch("/api/network/timezones").then((r) => r.json())) || []; } catch { /* picker still accepts a typed zone */ }
  }
  if (state.tab === "network") renderPanel();
}

// loadOverrides fetches the read-only override-layer view (RFC-0005) for the
// Expert tab. Failures degrade to an empty view rather than blocking the tab.
async function loadOverrides() {
  try {
    overridesData = await fetch("/api/overrides").then((r) => r.json());
  } catch {
    overridesData = { dir: "", overrides: [], warnings: [] };
  }
  if (state.tab === "expert") renderPanel();
}

// --- Connection profiles (RFC-0006) -------------------------------------
async function loadProfiles() {
  try {
    profiles = await fetch("/api/profiles").then((r) => r.json());
  } catch {
    profiles = [];
  }
  if (state.tab === "profiles") renderPanel();
}

async function saveProfile() {
  const el = document.getElementById("prof-name");
  const name = (el && el.value || "").trim();
  if (!name) { el && el.focus(); return; }
  profileBusy = true; renderPanel();
  try {
    const r = await fetch("/api/profiles", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
    if (!r.ok) alert("Save failed: " + (await r.text()));
  } catch (e) { alert("Save failed: " + e); }
  profileBusy = false;
  await loadProfiles();
}

async function activateProfile(name) {
  if (!confirm(`Activate "${name}"? This writes its saved modes & networks and restarts the stack.`)) return;
  profileBusy = true; renderPanel();
  try {
    const r = await fetch("/api/profiles/" + encodeURIComponent(name) + "/activate", { method: "POST" });
    if (!r.ok) alert("Activate failed: " + (await r.text()));
  } catch (e) { alert("Activate failed: " + e); }
  profileBusy = false;
  await loadProfiles();
}

async function deleteProfile(name) {
  if (!confirm(`Delete profile "${name}"? This does not change the live configuration.`)) return;
  profileBusy = true; renderPanel();
  try {
    const r = await fetch("/api/profiles/" + encodeURIComponent(name), { method: "DELETE" });
    if (!r.ok) alert("Delete failed: " + (await r.text()));
  } catch (e) { alert("Delete failed: " + e); }
  profileBusy = false;
  await loadProfiles();
}

// exportProfile downloads the scrubbed artifact via a temporary anchor so the
// browser's own save dialog handles the file (no data ever leaves the node except
// to the operator's disk).
function exportProfile(name) {
  const a = document.createElement("a");
  a.href = "/api/profiles/" + encodeURIComponent(name) + "/export";
  a.download = name.replace(/[^a-zA-Z0-9_-]+/g, "-") + ".waypoint-profile.json";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

async function importProfile(file) {
  if (!file) return;
  profileBusy = true; renderPanel();
  try {
    const text = await file.text();
    let r = await fetch("/api/profiles/import", { method: "POST", headers: { "Content-Type": "application/json" }, body: text });
    if (r.status === 409) {
      if (confirm("A profile with that name already exists. Overwrite it?")) {
        r = await fetch("/api/profiles/import?overwrite=1", { method: "POST", headers: { "Content-Type": "application/json" }, body: text });
      } else { profileBusy = false; renderPanel(); return; }
    }
    if (!r.ok) alert("Import failed: " + (await r.text()));
  } catch (e) { alert("Import failed: " + e); }
  profileBusy = false;
  await loadProfiles();
}

// --- Config import / migration (RFC-0007) --------------------------------
// buildImportBody turns the remembered input into a fetch body + headers: a JSON
// {dir} for a mounted path, or multipart for uploaded files.
function importFetchInit(input) {
  if (input.dir != null) {
    return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ dir: input.dir }) };
  }
  const fd = new FormData();
  for (const f of input.files) fd.append("files", f, f.name);
  return { method: "POST", body: fd }; // browser sets the multipart Content-Type + boundary
}

async function runImportScan(input) {
  importInput = input;
  importBusy = true; importScan = null; renderPanel();
  try {
    const r = await fetch("/api/import/scan", importFetchInit(input));
    if (!r.ok) { alert("Scan failed: " + (await r.text())); importInput = null; }
    else importScan = await r.json();
  } catch (e) { alert("Scan failed: " + e); importInput = null; }
  importBusy = false; renderPanel();
}

async function applyImport() {
  if (!importInput) return;
  if (!confirm("Import the scanned config? This overwrites your current mode & network settings. You'll review and Apply afterward.")) return;
  importBusy = true; renderPanel();
  try {
    const r = await fetch("/api/import/apply", importFetchInit(importInput));
    if (!r.ok) alert("Import failed: " + (await r.text()));
    else {
      alert("Imported. Review the settings, then press Apply to regenerate configs and restart the stack.");
      importScan = null; importInput = null;
      await load(); // refresh the editor from the freshly-written store
    }
  } catch (e) { alert("Import failed: " + e); }
  importBusy = false;
  renderPanel();
}

// netConnByType resolves (creating if needed) the single managed connection of a
// type — the editor surfaces one Ethernet + one Wi-Fi profile.
function netConnByType(type) { return type === "wifi" ? netWifiConn() : netEthConn(); }

async function rescanWiFi() {
  const btn = document.getElementById("net-scan-refresh");
  if (btn) { btn.textContent = "SCANNING…"; btn.disabled = true; }
  try { netScanResults = (await fetch("/api/network/wifi/scan").then((r) => r.json())) || []; } catch { /* keep previous list */ }
  if (state.tab === "network") renderPanel();
}

// applyNetwork saves the edited config to the store then triggers the guarded
// apply: it never applies directly. The response carries a confirm token +
// deadline; the token is stashed in sessionStorage (so a page reload can still
// confirm) and the countdown bar is shown. If the operator does nothing, the
// server rolls back on its own timer.
async function applyNetwork() {
  if (netApplying || !netEdit) return;
  netApplying = true;
  const btn = document.getElementById("net-apply");
  if (btn) { btn.textContent = "APPLYING…"; btn.disabled = true; }
  try {
    const payload = {
      connections: netEdit.connections.filter(netPersist).map(netToPayload),
      vlans: (netEdit.vlans || []).map(vlanToPayload),
    };
    let r = await fetch("/api/network/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    if (!r.ok) throw new Error((await r.text()).trim());
    r = await fetch("/api/network/apply", { method: "POST" });
    if (!r.ok) throw new Error((await r.text()).trim());
    const j = await r.json();
    sessionStorage.setItem("wp-net-token", j.token);
    netDirty = false;
    showNetworkConfirmBar(j.deadline, j.token);
    banner("Network change applied — confirm to keep it before it reverts.", "ok");
  } catch (err) {
    banner("Network apply failed: " + String(err.message || err), "bad");
  } finally {
    netApplying = false;
    await loadNetwork();
  }
}

// applyHost saves and applies the host/NTP settings DIRECTLY (no guard — they
// can't strand the node). Idempotent server-side, so a no-op apply is harmless.
async function applyHost() {
  if (netApplyingHost || !netEdit) return;
  netApplyingHost = true;
  const btn = document.getElementById("host-apply");
  if (btn) { btn.textContent = "APPLYING…"; btn.disabled = true; }
  try {
    const payload = { host: { hostname: (netEdit.host.hostname || "").trim(), timezone: (netEdit.host.timezone || "").trim() }, ntp: { enabled: !!netEdit.ntp.enabled, servers: (netEdit.ntp.servers || []).slice() } };
    let r = await fetch("/api/network/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    if (!r.ok) throw new Error((await r.text()).trim());
    r = await fetch("/api/network/host/apply", { method: "POST" });
    if (!r.ok) throw new Error((await r.text()).trim());
    const j = await r.json();
    netHostDirty = false;
    banner(j.changed ? "Host settings applied." : "Host settings already in effect (no change).", "ok");
  } catch (err) {
    banner("Host apply failed: " + String(err.message || err), "bad");
  } finally {
    netApplyingHost = false;
    await loadNetwork();
  }
}

// text edits update the working copy; toggles flip a bool and re-render.
document.getElementById("panels").addEventListener("input", (e) => {
  const t = e.target;
  if (!t.dataset) return;
  // --- network editable fields (connections + VLANs: guarded apply) ---
  if (t.dataset.netmethod != null) {
    netIPv4Target(t.dataset.netmethod).method = t.value;
    netMarkDirty(); renderPanel();
    return;
  }
  if (t.dataset.netip != null) { netIPv4Target(t.dataset.netip)[t.dataset.ipkey] = t.value.trim(); netMarkDirty(); return; }
  if (t.dataset.netdns != null) { netIPv4Target(t.dataset.netdns).dns = textToList(t.value); netMarkDirty(); return; }
  if (t.dataset.netsearch != null) { netIPv4Target(t.dataset.netsearch).search_domains = textToList(t.value); netMarkDirty(); return; }
  if (t.dataset.netwifi != null) { netConnByType(t.dataset.netwifi)[t.dataset.wkey] = t.value; netMarkDirty(); return; }
  if (t.dataset.netpsk != null) { netConnByType(t.dataset.netpsk).psk = t.value; netMarkDirty(); return; }
  if (t.dataset.vlanf != null) { netEdit.vlans[+t.dataset.vlanf][t.dataset.vkey] = t.value; netMarkDirty(); return; }
  // --- host/NTP fields (direct apply) ---
  if (t.dataset.hostf != null) { netEdit.host[t.dataset.hkey] = t.value; netMarkHostDirty(); return; }
  if (t.dataset.ntpservers != null) { netEdit.ntp.servers = textToList(t.value); netMarkHostDirty(); return; }
  // --- mode buses (RFC-0003) ---
  if (t.dataset.busname != null) { const b = (edit.buses || []).find((x) => x.id === t.dataset.busname); if (b) { b.name = t.value; dirty.add("buses"); } return; }
  if (t.dataset.tgmap != null) { const a = edit.attachments[+t.dataset.tgmap]; a._tgrows[+t.dataset.tgi][t.dataset.tgk] = t.value; dirty.add("attachments"); return; }
  if (t.dataset.attach != null) { edit.attachments[+t.dataset.attach][t.dataset.akey] = t.value; dirty.add("attachments"); return; }
  if (t.dataset.sec) {
    let v = t.value;
    if (t.dataset.kind === "mhz") { const f = parseFloat(v); v = isNaN(f) ? "" : String(Math.round(f * 1e6)); }
    // int-typed fields (e.g. history.retention_days) must reach the store as a JSON
    // number: a blank or non-numeric entry floors to 0 (which the store reads as
    // "keep forever" for retention).
    else if (t.dataset.kind === "int") { const n = parseInt(v, 10); v = isNaN(n) || n < 0 ? 0 : n; }
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
  // LCD rows/cols selects — rows changes the line-input count and cols changes the
  // preview width, so either re-renders the page cards.
  if (t.dataset.lcdDim != null) {
    setField("lcd", t.dataset.lcdDim, t.value);
    renderPanel();
    return;
  }
  // LCD page name / duration.
  if (t.dataset.lcdpage != null) {
    edit.lcd.pages[+t.dataset.lcdpage][t.dataset.lcdkey] = t.value;
    dirty.add("lcd"); refreshActions();
    return;
  }
  // LCD page line: update the model, refresh this page's token warning + preview.
  if (t.dataset.lcdline != null) {
    const pi = +t.dataset.lcdline, ri = +t.dataset.lcdrow;
    ensureLcdLine(pi, ri);
    edit.lcd.pages[pi].lines[ri] = t.value;
    dirty.add("lcd"); updatePageWarning(pi); updatePagePreview(pi); refreshActions();
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
  if (t.dataset.rttg != null) { edit.routes[+t.dataset.rttg].tg = tgNumber(t.value); dirty.add("routes"); refreshActions(); return; }
  if (t.dataset.rtnet != null) { edit.routes[+t.dataset.rtnet].network = t.value; dirty.add("routes"); refreshActions(); return; }
  // per-network field, bound by network type (created on demand).
  if (t.dataset.netf != null) {
    ensureNet(t.dataset.netf)[t.dataset.nkey] = t.value;
    dirty.add("networks"); refreshActions();
  }
});
document.getElementById("panels").addEventListener("click", (e) => {
  // --- network editable controls (separate state; guarded apply) ---
  const nh = e.target.closest("[data-nethidden]");
  if (nh) { const c = netConnByType(nh.dataset.nethidden); c.hidden = !c.hidden; netMarkDirty(); renderPanel(); return; }
  const nj = e.target.closest("[data-netjoin]");
  if (nj) {
    const c = netWifiConn();
    c.ssid = nj.dataset.netjoin;
    c.psk = ""; c.has_psk = false; // a joined network needs its own passphrase entered
    netMarkDirty(); renderPanel();
    const psk = document.querySelector('[data-netpsk="wifi"]');
    if (psk) psk.focus();
    return;
  }
  if (e.target.id === "net-scan-refresh") { rescanWiFi(); return; }
  if (e.target.id === "net-apply") { applyNetwork(); return; }
  // NTP enable switch (direct-apply state).
  const nnt = e.target.closest("[data-netntp]");
  if (nnt) { netEdit.ntp.enabled = !netEdit.ntp.enabled; netMarkHostDirty(); renderPanel(); return; }
  // VLAN add / remove (guarded-apply state).
  if (e.target.id === "vlan-add") {
    (netEdit.vlans = netEdit.vlans || []).push({ parent: "eth0", id: "", name: "", ipv4: { method: "auto", address: "", prefix: "", gateway: "", dns: [], search_domains: [] } });
    netMarkDirty(); renderPanel(); return;
  }
  const vd = e.target.closest("[data-vlandel]");
  if (vd) { netEdit.vlans.splice(+vd.dataset.vlandel, 1); netMarkDirty(); renderPanel(); return; }
  if (e.target.id === "host-apply") { applyHost(); return; }
  // --- connection profiles (RFC-0006) ---
  if (e.target.id === "prof-save") { saveProfile(); return; }
  const pa = e.target.closest("[data-prof-activate]");
  if (pa) { activateProfile(pa.dataset.profActivate); return; }
  const px = e.target.closest("[data-prof-export]");
  if (px) { exportProfile(px.dataset.profExport); return; }
  const pd = e.target.closest("[data-prof-delete]");
  if (pd) { deleteProfile(pd.dataset.profDelete); return; }
  // --- config import / migration (RFC-0007) ---
  if (e.target.id === "import-scan-dir") {
    const el = document.getElementById("import-dir");
    const dir = (el && el.value || "").trim();
    if (!dir) { el && el.focus(); return; }
    runImportScan({ dir });
    return;
  }
  if (e.target.id === "import-apply") { applyImport(); return; }
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
  // LCD per-page interrupt toggle (take over the panel on activity vs rotate).
  const lpi = e.target.closest("[data-lcdpageint]");
  if (lpi) { const p = edit.lcd.pages[+lpi.dataset.lcdpageint]; p.interrupt = !p.interrupt; dirty.add("lcd"); renderPanel(); refreshActions(); return; }
  // LCD reorder page (swap with the neighbour in the given direction).
  const lpm = e.target.closest("[data-lcdmove]");
  if (lpm) {
    const i = +lpm.dataset.lcdpageidx, j = lpm.dataset.lcdmove === "up" ? i - 1 : i + 1;
    const ps = edit.lcd.pages;
    if (j >= 0 && j < ps.length) { [ps[i], ps[j]] = [ps[j], ps[i]]; dirty.add("lcd"); renderPanel(); refreshActions(); }
    return;
  }
  // LCD remove page.
  const lpd = e.target.closest("[data-lcdpagedel]");
  if (lpd) { edit.lcd.pages.splice(+lpd.dataset.lcdpagedel, 1); dirty.add("lcd"); renderPanel(); refreshActions(); return; }
  // LCD add page.
  if (e.target.id === "lcd-add-page") {
    (edit.lcd.pages = edit.lcd.pages || []).push({ enabled: true, name: "Page " + (edit.lcd.pages.length + 1), duration: "8", interrupt: false, lines: [] });
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
  // --- mode buses (RFC-0003) ---
  if (e.target.id === "bus-create") { createBus(); return; }
  if (e.target.id === "bus-migrate") { runMigration(); return; }
  const ben = e.target.closest("[data-busen]");
  if (ben) { toggleBus(ben.dataset.busen); return; }
  const bdel = e.target.closest("[data-busdel]");
  if (bdel) { deleteBus(bdel.dataset.busdel); return; }
  const aopen = e.target.closest("[data-attachopen]");
  if (aopen) { openAttachPicker(aopen.dataset.attachopen); return; }
  if (e.target.closest("[data-attachcancel]")) { attachPicker = null; renderPanel(); return; }
  const apick = e.target.closest("[data-attachpick]");
  if (apick) { attachMode(attachPicker.busId, apick.dataset.attachpick); return; }
  const adel = e.target.closest("[data-attachdel]");
  if (adel) { detachMode(+adel.dataset.attachdel); return; }
  const abool = e.target.closest("[data-attachbool]");
  if (abool) { const a = edit.attachments[+abool.dataset.attachbool]; a[abool.dataset.abkey] = !a[abool.dataset.abkey]; dirty.add("attachments"); renderPanel(); refreshActions(); return; }
  const tgadd = e.target.closest("[data-tgadd]");
  if (tgadd) { const a = edit.attachments[+tgadd.dataset.tgadd]; (a._tgrows = a._tgrows || []).push({ from: "", to: "" }); dirty.add("attachments"); renderPanel(); refreshActions(); return; }
  const tgdel = e.target.closest("[data-tgdel]");
  if (tgdel) { const a = edit.attachments[+tgdel.dataset.tgdel]; a._tgrows.splice(+tgdel.dataset.tgi, 1); dirty.add("attachments"); renderPanel(); refreshActions(); return; }
  // --- via-peer (remote) attach ---
  const apeer = e.target.closest("[data-attachpeer]");
  if (apeer && attachPicker) { openRemoteAttachPicker(attachPicker.busId, apeer.dataset.attachpeer); return; }
  const arpick = e.target.closest("[data-attachrpick]");
  if (arpick && attachPicker) { const [pid, mode] = arpick.dataset.attachrpick.split("|"); attachRemote(attachPicker.busId, pid, mode); return; }
  const rdel = e.target.closest("[data-remotedel]");
  if (rdel) { detachRemote(rdel.dataset.remotedel); return; }
  // --- LAN peering (RFC-0016) ---
  if (e.target.id === "peer-discover") { discoverPeers(); return; }
  if (e.target.id === "peer-pair-manual") { const el = document.getElementById("peer-manual"); const v = (el && el.value || "").trim(); if (v) pairWith(v); else el && el.focus(); return; }
  const ppair = e.target.closest("[data-peerpair]");
  if (ppair) { pairWith(ppair.dataset.peerpair); return; }
  const prev = e.target.closest("[data-peerrevoke]");
  if (prev) { revokePeer(prev.dataset.peerrevoke, prev.dataset.peername); return; }
});
// Pairing modal (its own overlay element, outside #panels).
document.addEventListener("click", (e) => {
  const cp = e.target.closest("[data-paircopy]");
  if (cp) { copyText(cp.dataset.paircopy); cp.textContent = "Copied"; setTimeout(() => { cp.textContent = "Copy"; }, 1200); return; }
  const pc = e.target.closest("[data-pairconfirm]");
  if (pc) { confirmPair(pc.dataset.pairconfirm, ""); return; }
  const pe = e.target.closest("[data-pairenter]");
  if (pe) { const el = document.getElementById("pair-code-input"); confirmPair(pe.dataset.pairenter, (el && el.value || "").trim()); return; }
  const px = e.target.closest("[data-paircancel]");
  if (px) { cancelPair(px.dataset.paircancel); return; }
});
function copyText(t) { try { navigator.clipboard.writeText(t); } catch (e) { /* clipboard blocked */ } }
// Profile import file picker (fires "change", not "click").
document.getElementById("panels").addEventListener("change", (e) => {
  if (e.target.id === "prof-import-file") {
    importProfile(e.target.files && e.target.files[0]);
    e.target.value = ""; // allow re-importing the same file
  }
  // Incumbent config-file upload → scan (RFC-0007). Keep the FileList to replay on Import.
  if (e.target.id === "import-files") {
    if (e.target.files && e.target.files.length) runImportScan({ files: e.target.files });
  }
});
// Keyboard support for the network role="switch" pills (Wi-Fi hidden, NTP enable):
// Enter/Space toggle them like a native checkbox, and focus is restored after the
// re-render so the keyboard user stays put.
document.getElementById("panels").addEventListener("keydown", (e) => {
  const t = e.target;
  if (!t.dataset) return;
  if (e.key !== "Enter" && e.key !== " ") return;
  if (t.dataset.nethidden != null) {
    e.preventDefault();
    const type = t.dataset.nethidden;
    const c = netConnByType(type); c.hidden = !c.hidden; netMarkDirty(); renderPanel();
    const again = document.querySelector('[data-nethidden="' + type + '"]');
    if (again) again.focus();
  } else if (t.dataset.netntp != null) {
    e.preventDefault();
    netEdit.ntp.enabled = !netEdit.ntp.enabled; netMarkHostDirty(); renderPanel();
    const again = document.querySelector("[data-netntp]");
    if (again) again.focus();
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
initBusEvents(); // live bus_busy surfacing on the Buses tab (RFC-0003 §5)
// LAN peering (RFC-0016): a modal overlay for the active pairing, and a poll so a
// responder learns of an incoming pairing request while on the tab.
(function initPeeringUI() {
  const el = document.createElement("div");
  el.id = "peer-modal";
  el.hidden = true;
  document.body.appendChild(el);
  startPeeringPoll();
})();
