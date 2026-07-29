// Waypoint settings page. Reads the node's config from /api/config (served from
// the store) and writes edits back: PUT /api/config/{section} merges the changed
// fields into the store, then POST /api/config/apply regenerates the daemons'
// INIs and restarts them. Values are never hard-coded and never patched into
// INIs — the store is authoritative (RFC-0001).

// A tab is an id and a two-letter glyph; every word an operator reads — label,
// sidebar subtitle, breadcrumb, page title, description — is in the catalog under
// "tab.<id>.*". Adding a tab is an entry here plus five catalog keys.
//
// The breadcrumb still carries the taxonomy: its prefix ("SYSTEM / GENERAL" ->
// SYSTEM) is the nav group, so renderNav files a tab straight from tabCrumbKey's
// English base. That lookup deliberately reads the base catalog rather than the
// active one — a translated group name would not match NAV_GROUPS, and the
// grouping is structure, not copy.
//
// The eight per-mode panels are not top-level tabs: they are sub-tabs of the single
// "modes" entry below (see MODE_SUBS). Their old ids still resolve as deep links.
const TABS = [
  { id: "general",      tag: "RF" },
  { id: "hardware",     tag: "HW" },
  { id: "setup",        tag: "SU" },
  { id: "lcd",          tag: "LC" },
  { id: "station",      tag: "ST" },
  { id: "modes",        tag: "MD" },
  { id: "brandmeister", tag: "BM" },
  { id: "network",      tag: "NW" },
  { id: "gateways",     tag: "GW" },
  { id: "profiles",     tag: "PF" },
  { id: "updates",      tag: "UP" },
  { id: "expert",       tag: "SY" },
];

// Sidebar group order. Anything whose crumb prefix is not listed here still renders,
// appended after these in first-seen order, so a new crumb can never drop a tab.
const NAV_GROUPS = ["SYSTEM", "MODES", "NETWORKS", "ADMIN"];

// The Modes tab's sub-tabs. Each renders one of the existing per-mode panel builders
// unchanged; `id` doubles as the legacy top-level tab id for deep links (#dmr →
// #modes/dmr) and as the key into edit.modes for the enable toggles.
const MODE_SUBS = [
  { id: "dstar",  label: "D-Star",        crumb: "D-STAR",        panel: () => panelDStar() },
  { id: "dmr",    label: "DMR",           crumb: "DMR",           panel: () => panelDmr() },
  { id: "ysf",    label: "System Fusion", crumb: "SYSTEM FUSION", panel: () => panelYSF() },
  { id: "p25",    label: "P25",           crumb: "P25",           panel: () => panelP25() },
  { id: "nxdn",   label: "NXDN",          crumb: "NXDN",          panel: () => panelNXDN() },
  { id: "m17",    label: "M17",           crumb: "M17",           panel: () => panelM17() },
  { id: "pocsag", label: "POCSAG",        crumb: "POCSAG",        panel: () => panelPocsag() },
  { id: "fm",     label: "FM",            crumb: "FM",            panel: () => panelFm() },
];

// Which settings have inline help (#135), keyed by the "section.field" pair every
// control already carries in data-sec/data-key (or data-toggle). A field not
// listed here simply renders without a help affordance, so the set can be filled
// in over time without touching a panel.
//
// The help text itself is in the catalog under "help.<section>.<field>" — adding
// help is a key here and a string there.
//
// House style: say what the setting does and when an operator would change it, and
// name the INI key it renders to where that is the fastest way for an experienced
// operator to orient. Don't restate the label.
const HELP = new Set([
  // --- station identity + radio ---
  "general.callsign",
  "general.id",
  "general.location",
  "general.url",
  "general.power",
  "general.duplex",
  // --- modem / RF ---
  "modem.rx_freq_hz",
  "modem.tx_freq_hz",
  "modem.port",
  "modem.board",
  "modem.tcxo_hz",
  "modem.uart_speed",
  "modem.rx_offset",
  "modem.tx_offset",
  "modem.rx_level",
  "modem.tx_level",
  "modem.rf_level",
  "modem.rx_dc_offset",
  "modem.tx_dc_offset",
  "modem.rx_invert",
  "modem.tx_invert",
  "modem.ptt_invert",
  "modem.dmr_delay",
  "modem.rssi_mapping_file",
  "modem.dstar_tx_level",
  "modem.dmr_tx_level",
  "modem.ysf_tx_level",
  "modem.p25_tx_level",
  "modem.nxdn_tx_level",
  "modem.pocsag_tx_level",
  "modem.fm_tx_level",
  // --- DMR ---
  "dmr.color_code",
  "dmr.id",
  "dmr.embedded_lc_only",
  "dmr.self_only",
  "dmr.beacons",
  "dmr.dump_ta_data",
  "dmrnet.slot1",
  "dmrnet.slot2",
  // --- mode enables ---
  "modes.dmr",
  "modes.dstar",
  "modes.ysf",
  "modes.p25",
  "modes.nxdn",
  "modes.m17",
  "modes.pocsag",
  "modes.fm",
  // --- D-Star ---
  "dstar.module",
  "dstar.self_only",
  "dstar.remote_gateway",
  "dstargw.reflector",
  "dstargw.ircddb_hostname",
  "dstargw.ircddb_username",
  "dstargw.ircddb_password",
  "dstargw.reflector_reconnect",
  "dstargw.dplus",
  "dstargw.dplus_login",
  "dstargw.dextra",
  "dstargw.dcs",
  "dstargw.xlx",
  // --- System Fusion ---
  "ysf.low_deviation",
  "ysf.self_only",
  "ysf.remote_gateway",
  "ysf.tx_hang",
  "ysf.mode_hang",
  "ysfgw.startup",
  "ysfgw.ysf_network",
  "ysfgw.fcs_network",
  "ysfgw.ycs_network",
  "ysfgw.wiresx_passthrough",
  "ysfgw.revert",
  "ysfgw.inactivity_timeout",
  "ysfgw.aprs",
  "ysfgw.suffix",
  "ysfgw.enable_dgid",
  "ysfgw.upper_hostfiles",
  // --- P25 / NXDN / M17 ---
  "p25.nac",
  "p25.self_only",
  "p25.override_uid_check",
  "p25.remote_gateway",
  "p25gw.static",
  "p25gw.voice",
  "p25gw.rf_hang_time",
  "p25gw.net_hang_time",
  "nxdn.ran",
  "nxdn.self_only",
  "nxdn.remote_gateway",
  "nxdngw.static",
  "nxdngw.voice",
  "nxdngw.rf_hang_time",
  "nxdngw.net_hang_time",
  "m17.can",
  "m17.self_only",
  "m17.allow_encryption",
  "m17gw.startup",
  "m17gw.suffix",
  "m17gw.voice",
  "m17gw.revert",
  "m17gw.hang_time",
  // --- FM ---
  "fm.ctcss",
  "fm.timeout",
  "fm.kerchunk_time",
  "fm.rf_audio_boost",
  "fm.ext_audio_boost",
  "fm.access_mode",
  // --- POCSAG ---
  "pocsag.frequency",
  "pocsag.server",
  "pocsag.callsign",
  "pocsag.auth_key",
  "pocsag.whitelist",
  "pocsag.blacklist",
  // --- station ID + history ---
  "station_id.enable",
  "station_id.time_mins",
  "station_id.callsign",
  "station_id.tx_level",
  "history.retention_days",
  // --- updates ---
  "update.check_enabled",
  "update.auto_apply",
  "update.channel",
  "update.quiet_window",
  // --- display / LCD ---
  "display.port",
  "display.hd44780_rows",
  "display.hd44780_cols",
  "display.hd44780_i2c_addr",
  "lcd.enabled",
  "lcd.activity_interrupt",
  "lcd.i2c_bus",
  "lcd.i2c_address",
  "lcd.scroll_speed",
  "lcd.linger_secs",
]);

const THEMES = [
  { key: "phosphor", color: "#35d07f", attr: "" },
  { key: "amber",    color: "#f0a935", attr: "amber" },
  { key: "ice",      color: "#4db8ff", attr: "ice" },
];

let state = { tab: "general", sub: MODE_SUBS[0].id, config: null, health: null };
let edit = {};              // section -> {field: value} working copy
let dirty = new Set();      // sections with unsaved changes
let applying = false;
let ysfRefs = [];           // cached YSF reflector list for the startup picker
let p25Refs = [];           // cached P25 talkgroup list for the startup-TG picker
let nxdnRefs = [];          // cached NXDN talkgroup list for the startup-TG picker
let dstarRefs = [];         // cached D-Star reflector list for the startup picker
let m17Refs = [];           // cached M17 reflector list for the startup picker
let overridesData = null;   // GET /api/overrides — the override layer's effective records (read-only, RFC-0005)
let stackStatus = null;     // GET /api/update/stack — installed versions, available updates, history (RFC-0014)
let stackBusy = false;      // a check/apply request is in flight (guards double-clicks)
let stackPoll = null;       // interval while an apply runs, cleared when it settles
let profiles = null;        // saved connection profiles from /api/profiles (RFC-0006)
let hardware = null;        // GET /api/hardware — last detection, board table, UART diagnosis (#18)
let firmware = null;        // GET /api/flash — firmware catalog, the match or the refusal, the job (#19)
let fwBusy = false;         // a flash was asked for and has not come back yet
let fwStream = null;        // EventSource on /api/flash/events while a job runs
let calib = null;           // GET /api/cal — what can be swept, and the last sweep (#20)
let calBusy = false;
let calStream = null;       // EventSource on /api/cal/events while a sweep runs
let calLive = null;         // the newest progress frame, for the live readout
let hwBusy = false;         // a detect/adopt/repair is in flight
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

// --- navigation state ----------------------------------------------------
// Desktop renders the sidebar as collapsible groups; below 1024px the same tabs
// render as a grid of touch tiles with a back control (RFC-0009). The breakpoint is
// read through matchMedia rather than inferred, so a resize re-renders the nav with
// the right semantics instead of leaving a stale aria-expanded behind.
const NAV_NARROW = window.matchMedia("(max-width: 1023px)");
const NAV_OPEN_KEY = "wp-nav-groups";   // persisted group expansion (D2)
const MODE_SUB_KEY = "wp-mode-sub";     // persisted Modes sub-tab (D5)
let navOpen = loadNavOpen();  // group name -> expanded?
let navView = "panel";        // narrow-viewport view: "grid" (tiles) or "panel"

// Message catalogs — see i18n.js. Named msg() rather than t() because `t` is
// already this file's name for a tab object, a theme and an event target; a
// global of that name would be shadowed exactly where translation is needed.
const msg = (key, params) => WPI18n.t(key, params);

// A tab's copy, by id. Kept as one-liners so call sites read like the field
// access they replaced.
const tabLabel = (tab) => msg("tab." + tab.id + ".label");
const tabSub = (tab) => msg("tab." + tab.id + ".sub");
const tabTitle = (tab) => msg("tab." + tab.id + ".title");
const tabDesc = (tab) => msg("tab." + tab.id + ".desc");
const tabCrumb = (tab) => msg("tab." + tab.id + ".crumb");

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
    modem:   Object.assign(
      { rx_freq_hz: g.rx_freq_hz, tx_freq_hz: g.tx_freq_hz, port: g.modem_port, rx_offset: g.rx_offset, tx_offset: g.tx_offset },
      modemFrom(c.modem || {})),
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
    // Automatic CW identification (Station Settings tab). enable defaults ON when
    // the view omits it — a missing key must never read as "identification off",
    // which is a legal obligation no operator opted out of by accident. callsign
    // is the blank-means-inherit override; effective_callsign is derived server-side
    // and read-only here, so the UI never re-implements the inheritance rule.
    station_id: {
      enable: (c.station_id || {}).enable !== false,
      time_mins: (c.station_id || {}).time_mins || "10",
      callsign: (c.station_id || {}).callsign || "",
      tx_level: (c.station_id || {}).tx_level || "50",
    },
    // Software-update policy (Updates tab, RFC-0014). Channel + quiet window are
    // strings, check_enabled and auto_apply bools; saved through the normal Apply
    // flow. check_enabled defaults ON when the view omits it (#15) — a missing key
    // must never read as "checks off", which is an opt-out an operator never made.
    update: {
      channel: (c.update || {}).channel || "stable",
      check_enabled: (c.update || {}).check_enabled !== false,
      auto_apply: !!(c.update || {}).auto_apply,
      quiet_window: (c.update || {}).quiet_window || "04:00",
    },
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
  return `<div class="toggle-row"><span class="name">${msg("nodeLockRow.nodeLockPrivatePublic")}</span><button type="button" class="pill ${on ? "on" : "off"}" data-toggle="dmr.self_only" aria-pressed="${on}" aria-label="Node Lock (Private / Public)">${on ? "PRIVATE" : "PUBLIC"}</button></div>`;
}

// --- panels --------------------------------------------------------------
function panelGeneral() {
  const left = card(msg("general.stationIdentity"),
    input("general", "callsign", { label: "Callsign" }) +
    input("general", "id", { label: "DMR ID" }) +
    input("general", "location", { label: "Location" }) +
    input("general", "url", { label: "Dashboard URL" }));
  const radio = card(msg("general.radioFrequency"),
    input("modem", "rx_freq_hz", { label: "RX Frequency", kind: "mhz", unit: "MHz", accent: true }) +
    input("modem", "tx_freq_hz", { label: "TX Frequency", kind: "mhz", unit: "MHz", accent: true }) +
    input("modem", "port", { label: "Modem Port" }) +
    boardRow() +
    baudRow() +
    input("general", "power", { label: "RF Power", unit: "" }) +
    toggle("general", "duplex", msg("general.duplex"), msg("general.duplex2"), msg("general.simplex")) +
    note(msg("general.donTTypePort")));
  const cal = card(msg("general.calibration"),
    input("modem", "rx_offset", { label: "RX Offset", unit: "Hz" }) +
    input("modem", "tx_offset", { label: "TX Offset", unit: "Hz" }) +
    input("modem", "rx_level", { label: "RX Level", unit: "%" }) +
    input("modem", "tx_level", { label: "TX Level", unit: "%" }) +
    input("modem", "rf_level", { label: "RF Power", unit: "%" }) +
    note(msg("general.measureTheseRatherThan")));
  // The analog controls a full-size repeater board needs and a hotspot ignores
  // outright: MMDVM_HS's firmware does not read the invert flags or the DC
  // offsets at all. Saying so beside the fields is the difference between "this
  // slider does nothing" and "this slider is not for my board".
  const analog = card(msg("general.repeaterBoardOnly"),
    toggle("modem", "rx_invert", msg("general.rxInvert"), msg("general.inverted"), msg("general.normal")) +
    toggle("modem", "tx_invert", msg("general.txInvert"), msg("general.inverted"), msg("general.normal")) +
    toggle("modem", "ptt_invert", msg("general.pttInvert"), msg("general.inverted"), msg("general.normal")) +
    input("modem", "rx_dc_offset", { label: "RX DC Offset" }) +
    input("modem", "tx_dc_offset", { label: "TX DC Offset" }) +
    input("modem", "dmr_delay", { label: "DMR Delay" }) +
    input("modem", "rssi_mapping_file", { label: "RSSI Map" }) +
    note(msg("general.hotspotBoardIgnoresEvery")));
  const levels = card(msg("general.perModeTxLevels"),
    input("modem", "dstar_tx_level", { label: "D-Star", unit: "%" }) +
    input("modem", "dmr_tx_level", { label: "DMR", unit: "%" }) +
    input("modem", "ysf_tx_level", { label: "System Fusion", unit: "%" }) +
    input("modem", "p25_tx_level", { label: "P25", unit: "%" }) +
    input("modem", "nxdn_tx_level", { label: "NXDN", unit: "%" }) +
    input("modem", "pocsag_tx_level", { label: "POCSAG", unit: "%" }) +
    input("modem", "fm_tx_level", { label: "FM", unit: "%" }) +
    note(msg("general.leaveBlankFollowTx")));
  return `<div class="grid2">${left}<div class="stack">${radio}${cal}${levels}${analog}</div></div>`;
}

// modemFrom projects the calibration keys (#20). They live in the same store
// section as the frequencies, so they merge into the same edit object; they are
// listed separately here because they come from a different part of the view.
function modemFrom(m) {
  return {
    rx_level: m.rx_level, tx_level: m.tx_level,
    rx_dc_offset: m.rx_dc_offset, tx_dc_offset: m.tx_dc_offset,
    rf_level: m.rf_level, dmr_delay: m.dmr_delay,
    tx_invert: !!m.tx_invert, rx_invert: !!m.rx_invert, ptt_invert: !!m.ptt_invert,
    dstar_tx_level: m.dstar_tx_level, dmr_tx_level: m.dmr_tx_level,
    ysf_tx_level: m.ysf_tx_level, p25_tx_level: m.p25_tx_level,
    nxdn_tx_level: m.nxdn_tx_level, pocsag_tx_level: m.pocsag_tx_level,
    fm_tx_level: m.fm_tx_level,
    rssi_mapping_file: m.rssi_mapping_file,
  };
}

// boardRow is WPSD's "Radio/Modem" dropdown. The list comes from the daemon's
// board table (loaded with the hardware surface) so there is no second copy of
// it here; before that arrives, or on a node configured for a board this build
// does not know, the current value is still offered so selecting it is never
// lost.
function boardRow() {
  const cur = (edit.modem || {}).board || "";
  const boards = (hardware && hardware.boards) || [];
  let opts = `<option value=""${cur === "" ? " selected" : ""}>${msg("boardRow.notSet")}</option>`;
  let seen = false;
  opts += boards.map((b) => {
    if (b.id === cur) seen = true;
    const suffix = b.tcxo_label ? ` — ${b.tcxo_label}` : "";
    return `<option value="${esc(b.id)}"${b.id === cur ? " selected" : ""}>${esc(b.name)}${esc(suffix)}</option>`;
  }).join("");
  if (cur && !seen) opts += `<option value="${esc(cur)}" selected>${esc(cur)}</option>`;
  return row(msg("boardRow.radioModem"), `<select data-sec="modem" data-key="board">${opts}</select>`);
}

// baudRow is WPSD's Baudrate field. 115200 covers the whole launch tier; the
// others are here for reflashed and full-size boards.
function baudRow() {
  const cur = (edit.modem || {}).uart_speed || "115200";
  const speeds = ["115200", "230400", "460800"];
  if (!speeds.includes(cur)) speeds.unshift(cur);
  const opts = speeds.map((v) => `<option value="${esc(v)}"${v === cur ? " selected" : ""}>${esc(v)}</option>`).join("");
  return row(msg("baudRow.baudrate"), `<select data-sec="modem" data-key="uart_speed">${opts}</select>`);
}

function panelDmr() {
  const master = card(msg("dmr.dmrMaster"),
    toggle("modes", "dmr", msg("dmr.enabled")) +
    input("dmr", "color_code", { label: "Color Code", accent: true }) +
    input("dmr", "id", { label: "DMR ID" }));
  const slots = card(msg("dmr.timeSlotsAdvanced"),
    toggleRow("dmrnet", "slot1", msg("dmr.timeSlot1Enabled")) +
    toggleRow("dmrnet", "slot2", msg("dmr.timeSlot2Enabled")) +
    toggleRow("dmr", "embedded_lc_only", msg("dmr.embeddedLcOnly")) +
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

// The Modes tab (D4): the enable toggles as the header area, then a sub-tab strip
// over the existing per-mode panels. The panel builders themselves are untouched —
// this is navigation, not a panel refactor. Switching a sub-tab only re-renders, so
// `edit` and `dirty` carry across unchanged (D6).
function panelModesSection() {
  const cur = MODE_SUBS.find((m) => m.id === currentModeSub()) || MODE_SUBS[0];
  const tabs = MODE_SUBS.map((m) => {
    const on = m.id === cur.id;
    const enabled = !!(edit.modes || {})[m.id];
    // Roving tabindex: only the selected tab is in the Tab order, arrows move within
    // the strip (WAI-ARIA tabs pattern).
    return `<button type="button" role="tab" class="msub${enabled ? " live" : ""}"
      id="msub-${esc(m.id)}" data-modesub="${esc(m.id)}" aria-selected="${on}"
      aria-controls="mode-subpanel" tabindex="${on ? 0 : -1}">${esc(m.label)}<span class="msub-dot" aria-hidden="true"></span><span class="sr-only">${enabled ? " (enabled)" : " (disabled)"}</span></button>`;
  }).join("");
  return `
    <div class="mode-sec-t">${msg("modes.modeEnable")}</div>
    <p class="mode-sec-d">${msg("modes.turnModeHaveMmdvm")}</p>
    ${panelModes()}
    <div class="mode-sec-t mode-sec-gap">${msg("modes.modeSettings")}</div>
    <div class="mode-subs" role="tablist" aria-label="Mode settings">${tabs}</div>
    <div class="mode-subpanel" id="mode-subpanel" role="tabpanel" tabindex="0" aria-labelledby="msub-${esc(cur.id)}">${cur.panel()}</div>`;
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
  const control = card(msg("display.controlSoftware"),
    row(msg("display.radioControlSoftware"), `<input value="MMDVMHost" readonly>`) +
    row(msg("display.trxMode"), trxSel));

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
    row(msg("display.displayType"), `<select data-displaytype>${typeOpts}</select>`) +
    row(msg("display.port"), `<select data-sec="display" data-key="port">${portOpts}</select>`);

  // Nextion layout — only when a Nextion is selected.
  if (d.type === "Nextion") {
    const lay = d.nextion_layout || "0";
    const layOpts = [["0", "G4KLX"], ["2", "ON7LDS L2"], ["3", "ON7LDS L3"], ["4", "ON7LDS L3 HS"]]
      .map(([v, l]) => `<option value="${v}"${v === lay ? " selected" : ""}>${l}</option>`).join("");
    displayRows += row(msg("display.nextionLayout"), `<select data-sec="display" data-key="nextion_layout">${layOpts}</select>`);
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

  const display = card(msg("display.display"), displayRows);
  const hint = note(msg("display.mmdvmHostBuildDisplay"));
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
  return `<details class="lcd-legend"><summary>${msg("lcdLegend.tokenReference")}</summary><dl>${items}</dl></details>`;
}

function panelLCD() {
  const l = edit.lcd || (edit.lcd = lcdFrom({}));
  const rows = Math.max(1, parseInt(l.rows, 10) || 4);
  const cols = l.cols || "20";
  const panel = card(msg("lcd.panel"),
    lcdToggleRow("enabled", msg("lcd.driverEnabled"), msg("common.enabled"), msg("common.disabled")) +
    input("lcd", "i2c_bus", { label: "I2C bus" }) +
    input("lcd", "i2c_address", { label: "I2C address", accent: true }) +
    row(msg("lcd.rows"), lcdSelect("rows", [["2", "2 rows"], ["4", "4 rows"]], l.rows)) +
    row(msg("lcd.columns"), lcdSelect("cols", [["16", "16 columns"], ["20", "20 columns"]], l.cols)) +
    input("lcd", "scroll_speed", { label: "Scroll speed", unit: "ms" }) +
    lcdToggleRow("activity_interrupt", msg("lcd.interruptActivity"), "ON", "OFF") +
    input("lcd", "linger_secs", { label: "Interrupt linger", unit: "s" }));
  const help = note(msg("lcd.linesFillTokensE"));
  const disabled = l.enabled ? "" : note(msg("lcd.driverDisabledPagesAre"));
  const pages = (l.pages || []).map((p, i) => pageCard(p, i, rows, cols, (l.pages || []).length)).join("");
  const add = `<button type="button" class="btn ghost mini-btn" id="lcd-add-page">${msg("lcd.addPage")}</button>`;
  return `<div class="grid2">${panel}<div class="stack">${help}${lcdLegend()}${disabled}</div></div>` +
    `<div class="stack" style="margin-top:16px;">${pages || note(msg("lcd.noPagesYetAdd"))}${add}</div>`;
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
// Supply state of the downloaded reflector/master/talkgroup lists, from
// /api/hostlists (#138). An empty picker used to be indistinguishable from a
// picker whose list never downloaded; this is what lets a panel say which.
let hostLists = [];
function hostList(name) { return hostLists.find((h) => h.name === name); }

// hostlistNote renders an explanation above a picker when its list is not in good
// shape, and nothing at all when it is — a healthy list should not be narrated.
function hostlistNote(name) {
  const h = hostList(name);
  if (!h) return "";
  const when = h.last_success && !String(h.last_success).startsWith("0001")
    ? new Date(h.last_success).toLocaleString() : null;
  // Nothing to show: downloaded recently and has content.
  if (h.entries > 0 && !h.from_seed && !h.stale) return "";

  if (h.entries === 0 && !h.has_seed && h.last_error) {
    return note(`<b>${msg("hostlistNote.listCouldNotDownloaded")}</b> ` +
      `Every source failed${when ? ` — the last successful download was ${esc(when)}` : ", and it has never downloaded"}. ` +
      `You can still type a value in by hand. <span style="color:var(--muted)">${esc(shortErr(h.last_error))}</span>`);
  }
  if (h.from_seed) {
    return note(`<b>${msg("hostlistNote.showingListShippedWaypoint")}</b> ` +
      `The node has not managed to download a newer one${h.last_error ? "" : " yet"}, so entries added upstream since the release will be missing. ` +
      `You can still type a value in by hand.`);
  }
  if (h.stale && when) {
    return note(`<b>${msg("hostlistNote.listMayOutDate")}</b> It last downloaded on ${esc(when)}; refreshes since then have failed. ` +
      `<span style="color:var(--muted)">${esc(shortErr(h.last_error))}</span>`);
  }
  if (h.entries === 0) {
    return note(`<b>${msg("hostlistNote.listEmpty")}</b> It downloaded without error but contained no entries. You can still type a value in by hand.`);
  }
  return "";
}
// The stored error names every source tried, which is right for the API and too
// much for a panel; keep the first failure and say how many followed.
function shortErr(err) {
  const s = String(err || "");
  const parts = s.split("; ");
  return parts.length > 1 ? `${parts[0]} (and ${parts.length - 1} more)` : s;
}

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
  let opts = `<option value=""${cur === "" ? " selected" : ""}>${msg("essidSelect.none")}</option>`;
  for (let i = 1; i <= 99; i++) { const v = String(i).padStart(2, "0"); opts += `<option value="${v}"${v === cur ? " selected" : ""}>${v}</option>`; }
  return `<select data-netf="${type}" data-nkey="essid">${opts}</select>`;
}

function sectionHead(title, type, n) {
  return `<div class="card-head"><span class="sq"></span><span class="t">${title}</span>${enPill(type, n)}</div>`;
}

function panelBrandmeister() {
  const d = edit.dmr || (edit.dmr = {});
  const supply = hostlistNote("dmr_hosts") + hostlistNote("dmr_talkgroups");
  const bm = netOf("brandmeister"), dp = netOf("dmrplus"), sx = netOf("systemx"), tg = netOf("tgif"), xl = netOf("xlx");
  const primaryType = ((edit.networks || []).find((n) => n.primary) || {}).type || "brandmeister";
  const masterSel = [["brandmeister", "Brandmeister"], ["dmrplus", "DMR+ / FreeDMR / HBlink Network"], ["systemx", "SystemX"], ["tgif", "TGIF"]]
    .map(([v, l]) => `<option value="${v}"${v === primaryType ? " selected" : ""}>${l}</option>`).join("");

  const master = `<section class="card">
      <div class="card-head"><span class="sq"></span><span class="t">${msg("bm.dmrMaster")}</span></div>
      ${row(msg("bm.dmrMaster"), `<select data-dmrprimary>${masterSel}</select>`)}
    </section>`;

  const bmSec = `<section class="card">
      ${sectionHead(msg("bm.brandmeisterNetworkSettings"), "brandmeister", bm)}
      ${row(msg("bm.brandmeisterMaster"), masterSelect("brandmeister", "brandmeister", bm))}
      ${row(msg("bm.bmHotspotSecurity"), `<input data-netf="brandmeister" data-nkey="password" type="password" value="${esc(bm ? bm.password || "" : "")}" placeholder="${bm && bm.has_password ? "•••••• unchanged" : ""}">`)}
      ${row(msg("bm.brandmeisterNetworkEssid"), essidSelect("brandmeister", bm))}
      ${row(msg("bm.brandmeisterManager"), extLink("https://brandmeister.network/?page=hotspots", msg("bm.manageHotspotStaticTgs")))}
      ${row(msg("bm.brandmeisterDashboards"), extLink("https://brandmeister.network/", msg("bm.openDashboard")))}
    </section>`;

  const dpSec = `<section class="card">
      ${sectionHead(msg("bm.dmrFreedmrHblinkNetwork"), "dmrplus", dp)}
      ${row(msg("bm.dmrMaster"), masterSelect("dmrplus", "dmrplus", dp))}
      ${row(msg("bm.networkOptions"), netField("dmrplus", "options", dp, ""))}
      ${row(msg("bm.essid"), essidSelect("dmrplus", dp))}
    </section>`;

  const sxSec = `<section class="card">
      ${sectionHead(msg("bm.systemxNetworkSettings"), "systemx", sx)}
      ${row(msg("bm.systemxMaster"), masterSelect("systemx", "systemx", sx))}
      ${row(msg("bm.networkOptions"), netField("systemx", "options", sx, ""))}
      ${row(msg("bm.essid"), essidSelect("systemx", sx))}
      ${note(msg("bm.dialSystemxTalkgroups4"))}
    </section>`;

  const tgSec = `<section class="card">
      ${sectionHead(msg("bm.tgifNetworkSettings"), "tgif", tg)}
      ${row(msg("bm.tgifSecurityKey"), `<input data-netf="tgif" data-nkey="password" type="password" value="${esc(tg ? tg.password || "" : "")}" placeholder="${tg && tg.has_password ? "•••••• unchanged" : ""}">`)}
      ${row(msg("bm.essid"), essidSelect("tgif", tg))}
      ${row(msg("bm.tgifDashboards"), extLink("https://tgif.network/", msg("bm.openDashboard")))}
      ${note(msg("bm.dialTgifTalkgroups5"))}
    </section>`;

  const xlSec = `<section class="card">
      ${sectionHead(msg("bm.xlxNetworkSettings"), "xlx", xl)}
      ${row(msg("bm.xlxStartupTg"), netField("xlx", "xlx_startup", xl, ""))}
      ${row(msg("bm.xlxStartupModule"), netField("xlx", "xlx_module", xl, ""))}
      ${row(msg("bm.timeSlot"), slotSelect(xl && xl.xlx_slot, `data-netf="xlx" data-nkey="xlx_slot"`))}
    </section>`;

  const cc = d.color_code || "1";
  let ccOpts = "";
  for (let i = 0; i <= 15; i++) ccOpts += `<option value="${i}"${String(i) === String(cc) ? " selected" : ""}>${i}</option>`;
  const general = `<section class="card">
      <div class="card-head"><span class="sq"></span><span class="t">${msg("bm.generalDmrSettings")}</span></div>
      <div class="toggle-row"><span class="name">${msg("bm.dmrRoamingBeacon")}</span><button type="button" class="pill ${d.beacons ? "on" : "off"}" data-toggle="dmr.beacons" aria-pressed="${!!d.beacons}" aria-label="DMR Roaming Beacon">${d.beacons ? "ON" : "OFF"}</button></div>
      ${row(msg("bm.dmrColorCode"), `<select data-sec="dmr" data-key="color_code">${ccOpts}</select>`)}
      <div class="toggle-row"><span class="name">${msg("bm.dmrEmbeddedlconly")}</span><button type="button" class="pill ${d.embedded_lc_only ? "on" : "off"}" data-toggle="dmr.embedded_lc_only" aria-pressed="${!!d.embedded_lc_only}" aria-label="DMR EmbeddedLCOnly">${d.embedded_lc_only ? "ON" : "OFF"}</button></div>
      <div class="toggle-row"><span class="name">${msg("bm.dmrDumptadata")}</span><button type="button" class="pill ${d.dump_ta_data ? "on" : "off"}" data-toggle="dmr.dump_ta_data" aria-pressed="${!!d.dump_ta_data}" aria-label="DMR DumpTAData">${d.dump_ta_data ? "ON" : "OFF"}</button></div>
      ${nodeLockRow()}
      ${note(msg("bm.privateLocksTxNode"))}
    </section>`;

  return `${supply}<div class="stack">${master}${bmSec}${dpSec}${sxSec}${tgSec}${xlSec}${general}</div>${routingTable()}`;
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
    ? `<div class="route-head"><span>${msg("routingTable.slot")}</span><span>${msg("routingTable.dialedTg")}</span><span></span><span>${msg("routingTable.gateway")}</span><span></span></div>${rows}`
    : `<div class="route-empty">${msg("routingTable.noOverridesEveryTalkgroup")}</div>`;
  return `
    <div class="card" style="margin-top:16px;">
      <div class="route-title">${msg("routingTable.talkgroupRouting")}</div>
      <datalist id="dmr-tgs">${tgOpts}</datalist>
      ${body}
      <button class="btn ghost mini-btn" id="route-add"${nets.length ? "" : " disabled"}>${msg("routingTable.addRoute")}</button>
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
  const rows = card(msg("expert.versions"),
    `<div class="row"><label>${msg("expert.dashboardWaypointd")}</label><input value="${esc((h && h.version) || "—")}" readonly></div>` +
    `<div class="row"><label>${msg("expert.configStore")}</label><input value="${esc((c.sources && c.sources.store) || "—")}" readonly></div>`);
  return `<div class="grid2">${rows}${note(msg("expert.rawIniEditingPower"))}</div>${panelImport()}${panelOverrides()}`;
}

// panelImport is the Pi-Star / WPSD migration surface (RFC-0007 / issue #4): point
// Waypoint at a mounted card (a directory path) or upload the incumbent config
// files, Scan for a preview + report, then Import to bulk-write the store.
function panelImport() {
  const dis = importBusy ? " disabled" : "";
  const input = card(msg("import.importPiStarWpsd"), `
    <div class="row"><label>${msg("import.mountedCardPath")}</label>
      <input id="import-dir" placeholder="/mnt/sdcard  (or /media/…)" aria-label="Mounted incumbent card path"></div>
    <div style="display:flex; gap:12px; align-items:center; margin-top:6px; flex-wrap:wrap;">
      <button type="button" id="import-scan-dir"${dis} style="padding:8px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:6px;">${msg("import.scanDirectory")}</button>
      <label class="import-upload" style="font-family:var(--mono); font-size:12px; cursor:pointer; text-decoration:underline;">
        ${msg("import.uploadConfigFiles")}<input id="import-files" type="file" multiple style="display:none;"></label>
    </div>
    ${note(msg("import.copyIncumbentSEtc"))}`);

  const result = importScan ? importReport(importScan) : "";
  return `<div style="margin-top:14px;">${input}${result}</div>`;
}

function importReport(s) {
  const rep = s.report || {};
  const files = (rep.files || []).map((f) =>
    `<div class="row"><label>${esc(f.role)}</label><span style="font-family:var(--mono); font-size:12px;">${f.found ? "✓ " + esc(f.name) : "— not found"}</span></div>`).join("");
  const modes = (rep.modes || []).length ? esc((rep.modes || []).join(", ")) : "—";
  const nets = (rep.networks || []).map((n) =>
    `<div class="row"><label>${esc(n.name)}</label><span style="font-family:var(--mono); font-size:12px;">${esc(n.type)}${n.custom ? " · <b>custom routing preserved</b>" : ""}${n.enabled ? "" : " · disabled"}</span></div>`).join("") || note(msg("importReport.noDmrNetworksFound"));
  const unmapped = (rep.unmapped || []).length
    ? `<div class="card"><div class="card-head"><span class="sq"></span><span class="t">${msg("importReport.wonTCarryOver")}</span></div>${(rep.unmapped || []).map((u) => `<div class="row"><label>${esc(u.file)} · ${esc(u.section)}</label><span style="font-family:var(--mono); font-size:12px;">${esc(u.what)}</span></div>`).join("")}${note(msg("importReport.theseIncumbentFeaturesAren"))}</div>`
    : note(msg("importReport.everythingFoundMapsWaypoint"));
  const dis = importBusy ? " disabled" : "";
  const summary = card(msg("importReport.scanResult"),
    `<div class="row"><label>${msg("importReport.detected")}</label><span style="font-family:var(--mono); font-size:12px;">${esc(rep.platform || "unknown")}</span></div>` +
    `<div class="row"><label>${msg("importReport.modes")}</label><span style="font-family:var(--mono); font-size:12px;">${modes}</span></div>` + files);
  const apply = `<div style="display:flex; gap:12px; align-items:center; margin-top:10px;">
      <button type="button" id="import-apply"${dis} style="padding:9px 20px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">${msg("importReport.importIntoStore")}</button>
      <span class="note" style="margin:0;">${msg("importReport.overwritesCurrentModeAmp")} <b>${msg("importReport.apply")}</b> ${msg("importReport.goLive")}</span>
    </div>`;
  return `<div class="stack" style="margin-top:12px;">${summary}${card(msg("importReport.dmrNetworks"), nets)}${unmapped}</div>${apply}`;
}

// panelOverrides renders the read-only Override layer view (RFC-0005 / issue #2):
// the drop-in fragments that merge last into the generated INIs and survive every
// update. Data comes from GET /api/overrides, loaded lazily when the Expert tab
// opens. "Visible, not fought" — the operator sees exactly what their overrides
// change and which fragment wins.
function panelOverrides() {
  const d = overridesData;
  if (!d) return card(msg("overrides.overrides"), note(msg("overrides.loadingOverrideLayer")));
  const dirLine = note(`Override drop-ins live under <code>${esc(d.dir || "—")}/&lt;daemon&gt;.d/*.conf</code> ${msg("overrides.hostFileHooksUnder")} <code>${msg("overrides.ltHostfileGtPrepend")}</code> · <code>.append.d/</code>. They merge last into the generated files and are never touched by an update (<a href="https://github.com/KN4OQW/waypoint/issues/2">${msg("overrides.waypoint2")}</a>).`);
  const warn = (d.warnings && d.warnings.length)
    ? note(`<b>${d.warnings.length} malformed override line(s) ignored:</b><br>${d.warnings.map(esc).join("<br>")}`)
    : "";
  const list = d.overrides || [];
  if (!list.length) {
    return card(msg("overrides.overrides"), dirLine + note(msg("overrides.noOverridesActiveGenerated")) + warn);
  }
  // Group by daemon so each generated file's overrides read together.
  const byDaemon = {};
  list.forEach((o) => { (byDaemon[o.daemon] = byDaemon[o.daemon] || []).push(o); });
  const groups = Object.keys(byDaemon).sort().map((daemon) => {
    const rows = byDaemon[daemon].map(overrideRow).join("");
    return `<div class="card"><div class="card-head"><span class="sq"></span><span class="t">${esc(daemon)}</span></div>${rows}</div>`;
  }).join("");
  return card(msg("overrides.overrides"), dirLine + warn) + `<div class="stack">${groups}</div>`;
}

// overrideRow renders one effective override as a read-only status line:
// section · key, the rendered→effective transition (or REMOVED / ADDED), and the
// winning fragment filename (the provenance).
function overrideRow(o) {
  let change;
  if (o.unset) change = `<s>${esc(o.old)}</s> <b>${msg("overrideRow.removed")}</b>`;
  else if (o.added) change = `<b>${esc(o.new)}</b> <span class="note" style="display:inline">${msg("overrideRow.added")}</span>`;
  else change = `<s>${esc(o.old)}</s> → <b>${esc(o.new)}</b>`;
  const label = `[${o.section}] ${o.key}`;
  return `<div class="row"><label>${esc(label)}</label><span style="font-family:var(--mono); font-size:12px;">${change} <span class="note" style="display:inline; opacity:.7;">· ${esc(o.source)}</span></span></div>`;
}

// panelProfiles renders the Connection Profiles tab (RFC-0006 / issue #3): saved
// setups as cards with Activate / Export / Delete, a "save current setup as…"
// field, and Import. Data from GET /api/profiles (metadata only — never secrets).
function panelProfiles() {
  const save = card(msg("profiles.saveCurrentSetup"), `
    <div class="row">
      <label>${msg("profiles.profileName")}</label>
      <input id="prof-name" placeholder="e.g. BM DMR duplex" maxlength="64" aria-label="New profile name">
    </div>
    <div style="display:flex; gap:12px; align-items:center; margin-top:6px;">
      <button type="button" id="prof-save"${profileBusy ? " disabled" : ""} style="padding:8px 18px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">${msg("profiles.saveProfile")}</button>
      <label class="prof-import" style="font-family:var(--mono); font-size:12px; cursor:pointer; text-decoration:underline;">
        ${msg("profiles.importFile")}<input id="prof-import-file" type="file" accept=".json,application/json" style="display:none;">
      </label>
    </div>`);

  let listHTML;
  if (profiles === null) listHTML = note(msg("profiles.loadingProfiles"));
  else if (!profiles.length) listHTML = note(msg("profiles.noProfilesYetConfigure"));
  else listHTML = `<div class="stack">${profiles.map(profileCard).join("")}</div>`;

  const hint = note(msg("profiles.activatingProfileWritesIts"));
  return `<div class="grid2">${save}${hint}</div>${listHTML}`;
}

function profileCard(p) {
  const badge = p.active
    ? `<span class="prof-badge" style="font-family:var(--mono); font-size:11px; color:var(--accent); border:1px solid var(--accent); border-radius:4px; padding:1px 6px;">${msg("profileCard.active")}</span>`
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
    <div class="row"><label>${msg("profileCard.captured")}</label><span style="font-family:var(--mono); font-size:12px; opacity:.8;">${esc(p.updated_at || p.created_at || "—")}${fpText ? " · " + esc(fpText) : ""}</span></div>
    ${sens}
    <div style="display:flex; gap:10px; margin-top:8px; flex-wrap:wrap;">
      <button type="button" data-prof-activate="${esc(p.name)}"${dis} aria-label="Activate profile ${esc(p.name)}"${p.active ? " title='Already active'" : ""} style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">${msg("profileCard.activate")}</button>
      <button type="button" data-prof-export="${esc(p.name)}" aria-label="Export profile ${esc(p.name)}" style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:6px;">${msg("profileCard.export")}</button>
      <button type="button" data-prof-delete="${esc(p.name)}"${dis} aria-label="Delete profile ${esc(p.name)}" style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--fg); border:1px solid var(--line); border-radius:6px;">${msg("profileCard.delete")}</button>
    </div>
  </div>`;
}

function panelPending(what) {
  return note(`<b>${esc(what)}</b> ${msg("pending.settingsArenTWired")}<a href="https://github.com/KN4OQW/waypoint/issues/1">${msg("pending.waypoint1")}</a>).`);
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
  const editors = netEdit ? `${netHostCard()}${netEthCard()}${netWifiCard()}${netVlanCard()}` : note(msg("network.loadingNetworkConfiguration"));
  // The network Apply is SEPARATE from the radio Apply: it routes through the
  // confirm-or-revert guard (save → guarded apply → countdown). No direct-apply
  // escape hatch exists for host networking.
  const actions = `<div class="net-actions" style="display:flex; gap:12px; align-items:center; margin-top:6px;">
      <button type="button" id="net-apply"${netDirty ? "" : " disabled"} style="padding:8px 18px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">${msg("network.applyNetwork")}</button>
      <span class="note" style="margin:0;">${msg("network.appliesThroughConfirmRevert")}</span>
    </div>`;
  const hint = note(msg("network.editingHereWritesStore"));
  return `<div class="grid2">${live}<div class="stack">${editors}</div></div>${actions}${hint}`;
}

// netStatusSection is the live, read-only host-network state (unchanged from the
// status-only slice): what the box is actually doing right now.
function netStatusSection() {
  if (!netStatus) return card(msg("netStatusSection.liveStatus"), note(msg("netStatusSection.fetchingLiveNetworkStatus")));
  const s = netStatus;
  const host = card(msg("netStatusSection.hostLive"), statRow(msg("netStatusSection.hostname"), s.hostname) + statRow("NTP", ntpText(s.ntp)) + (s.wifi ? statRow(msg("netStatusSection.wiFi"), `${s.wifi.ssid} · ${s.wifi.signal}%`) : ""));
  const devs = (s.devices || []).filter((d) => d.type === "ethernet" || d.type === "wifi");
  const devCards = devs.length ? devs.map(deviceCard).join("") : note(msg("netStatusSection.noLiveEthernetWi"));
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
    statRow(msg("deviceCard.state"), d.state) +
    statRow(msg("deviceCard.profile"), conn) +
    statRow("IPv4", d.ipv4 + (d.method ? ` (${d.method})` : "")) +
    statRow(msg("deviceCard.gateway"), d.gateway) +
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
  const methodSel = row(msg("ipv4Editor.ipv4Method"),
    `<select data-netmethod="${esc(scope)}" aria-label="IPv4 method for ${esc(label)}">
       <option value="auto"${isStatic ? "" : " selected"}>${msg("ipv4Editor.dhcpAutomatic")}</option>
       <option value="manual"${isStatic ? " selected" : ""}>${msg("ipv4Editor.static")}</option>
     </select>`);
  const staticFields = isStatic
    ? row(msg("ipv4Editor.ipAddress"), `<input data-netip="${esc(scope)}" data-ipkey="address" value="${esc(ip.address)}" placeholder="192.168.1.50" aria-label="IP address for ${esc(label)}">`) +
      row(msg("ipv4Editor.prefixCidr"), `<input data-netip="${esc(scope)}" data-ipkey="prefix" value="${esc(ip.prefix)}" placeholder="24" aria-label="Network prefix length for ${esc(label)}">`) +
      row(msg("ipv4Editor.gateway"), `<input data-netip="${esc(scope)}" data-ipkey="gateway" value="${esc(ip.gateway)}" placeholder="192.168.1.1" aria-label="Default gateway for ${esc(label)}">`)
    : "";
  const dnsLabel = isStatic ? "DNS servers" : "DNS override (optional)";
  const dns = row(dnsLabel, `<input data-netdns="${esc(scope)}" value="${esc(listToText(ip.dns))}" placeholder="1.1.1.1, 8.8.8.8" aria-label="${esc(dnsLabel)} for ${esc(label)}">`) +
    (isStatic ? "" : note(msg("ipv4Editor.dhcpListingDnsServers")));
  const search = row(msg("ipv4Editor.searchDomainsOptional"), `<input data-netsearch="${esc(scope)}" value="${esc(listToText(ip.search_domains))}" placeholder="lan, example.org" aria-label="DNS search domains for ${esc(label)}">`);
  return methodSel + staticFields + dns + search;
}
function netEthCard() {
  const c = netEthConn();
  return card(msg("netEthCard.ethernetWaypointEth0"), ipv4Editor(c.ipv4, "conn:ethernet", "Ethernet"));
}
function netWifiCard() {
  const c = netWifiConn();
  const creds =
    row(msg("netWifiCard.ssidNetworkName"), `<input data-netwifi="wifi" data-wkey="ssid" value="${esc(c.ssid)}" placeholder="Your Wi-Fi name" aria-label="Wi-Fi SSID">`) +
    row(msg("netWifiCard.passphrase"), `<input data-netpsk="wifi" type="password" value="${esc(c.psk)}" placeholder="${c.has_psk ? "•••••• unchanged" : "Wi-Fi passphrase"}" aria-label="Wi-Fi passphrase">`) +
    switchRow(msg("netWifiCard.hiddenNetwork"), "nethidden", "wifi", c.hidden) +
    row(msg("netWifiCard.regulatoryCountry"), `<input data-netwifi="wifi" data-wkey="country" value="${esc(c.country)}" maxlength="2" placeholder="US" aria-label="Regulatory country code">`);
  return card(msg("netWifiCard.wiFiWaypointWifi"), creds) + netScanSection() + card(msg("netWifiCard.wiFiIpv4"), ipv4Editor(c.ipv4, "conn:wifi", "Wi-Fi"));
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
    row(msg("netHostCard.hostname"), `<input data-hostf="1" data-hkey="hostname" value="${esc(h.hostname)}" placeholder="${esc((netStatus && netStatus.hostname) || "waypoint")}" aria-label="Hostname">`) +
    row(msg("netHostCard.timezone"), `<input list="tz-list" data-hostf="1" data-hkey="timezone" value="${esc(h.timezone)}" placeholder="Region/City" aria-label="Timezone (type to search)"><datalist id="tz-list">${tzOptions}</datalist>${liveTz}`) +
    switchRow(msg("netHostCard.ntpTimeSync"), "netntp", "1", n.enabled) +
    row(msg("netHostCard.ntpServersOptional"), `<input data-ntpservers="1" value="${esc(listToText(n.servers))}" placeholder="pool.ntp.org, time.cloudflare.com" aria-label="NTP servers">`) +
    `<div style="margin-top:10px;"><button type="button" id="host-apply"${netHostDirty ? "" : " disabled"} style="padding:7px 16px; font-family:var(--mono); font-size:12px; cursor:pointer; background:var(--accent); color:#000; border:none; border-radius:6px;">${msg("netHostCard.applyHostSettings")}</button> <span class="note" style="margin:0;">${msg("netHostCard.appliesImmediatelyHostnameTimezone")}</span></div>`;
  return card(msg("netHostCard.hostTimeNtp"), body);
}

// netVlanCard: the VLAN list. Each VLAN is a tagged interface on a parent, with its
// own IPv4 block. VLANs render NM type=vlan keyfiles and go through the CONFIRM-OR-
// REVERT guard (a bad VLAN can cut the uplink), so they save with "Apply Network".
function netVlanCard() {
  const vlans = netEdit.vlans || [];
  const blocks = vlans.map((v, i) => {
    const head = `<div class="toggle-row"><span class="name">VLAN ${esc(v.id || "?")}${v.name ? " · " + esc(v.name) : ""} <span class="note" style="margin:0">(waypoint-vlan${esc(v.id || "?")})</span></span>` +
      `<button type="button" class="pill off" data-vlandel="${i}" style="cursor:pointer;" aria-label="Remove VLAN ${esc(v.id || "")}">${msg("netVlanCard.remove")}</button></div>`;
    const fields =
      row(msg("netVlanCard.parentInterface"), `<input data-vlanf="${i}" data-vkey="parent" value="${esc(v.parent)}" placeholder="eth0" aria-label="VLAN ${esc(v.id || "")} parent interface">`) +
      row(msg("netVlanCard.vlanId14094"), `<input data-vlanf="${i}" data-vkey="id" type="number" min="1" max="4094" value="${esc(v.id)}" placeholder="50" aria-label="VLAN id">`) +
      row(msg("netVlanCard.labelOptional"), `<input data-vlanf="${i}" data-vkey="name" value="${esc(v.name)}" placeholder="iot" aria-label="VLAN label">`) +
      ipv4Editor(v.ipv4, "vlan:" + i, "VLAN " + (v.id || (i + 1)));
    return `<div style="border-top:1px solid var(--line,rgba(128,128,128,0.25)); padding-top:8px; margin-top:8px;">${head}${fields}</div>`;
  }).join("");
  const empty = vlans.length ? "" : note(msg("netVlanCard.noVlansAddOne"));
  const add = `<div style="margin-top:10px;"><button type="button" id="vlan-add" style="padding:6px 14px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--accent); border:1px solid var(--accent); border-radius:6px;">${msg("netVlanCard.addVlan")}</button></div>`;
  return card(msg("netVlanCard.vlans"), empty + blocks + add);
}
function netScanSection() {
  const rows = (netScanResults || []).map((n) => {
    const lock = n.security ? "🔒" : "";
    const inuse = n.in_use ? ' <span style="color:var(--accent)">· connected</span>' : "";
    return `<div class="toggle-row"><span class="name">${esc(n.ssid)} ${lock} <span style="opacity:0.6">${n.signal}%</span>${inuse}</span>` +
      `<button type="button" class="pill off" data-netjoin="${esc(n.ssid)}" data-netsec="${esc(n.security)}" style="cursor:pointer;" aria-label="Join Wi-Fi network ${esc(n.ssid)}">JOIN</button></div>`;
  }).join("");
  const body = (netScanResults && netScanResults.length) ? rows : note(msg("netScanSection.noNetworksFoundYet"));
  const refresh = `<div style="margin-top:8px;"><button type="button" id="net-scan-refresh" style="padding:6px 14px; font-family:var(--mono); font-size:12px; cursor:pointer; background:transparent; color:var(--accent); border:1px solid var(--accent); border-radius:6px;">${msg("netScanSection.rescan")}</button></div>`;
  return card(msg("netScanSection.nearbyNetworks"), body + refresh);
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
  msg.innerHTML = `<b>${msg("showNetworkConfirmBar.keepTheseNetworkSettings")}</b> ${msg("showNetworkConfirmBar.nodeRevertsAutomatically")} <span id="net-count" aria-hidden="true">…</span>) unless you confirm it is still reachable.`;
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
  if (!token) { banner(msg("confirmNetwork.browserDoesnTHold"), "bad"); return; }
  try {
    const r = await fetch("/api/network/confirm", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token }) });
    if (!r.ok) throw new Error((await r.text()).trim());
    sessionStorage.removeItem("wp-net-token");
    hideNetworkConfirmBar();
    banner(msg("confirmNetwork.networkSettingsKept"), "ok");
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
  const migrate = card(msg("gateways.migrateRetiredBridges"),
    note(msg("gateways.oldPerModeBridges")) +
    `<div class="row"><button type="button" class="btn accent" id="bus-migrate">${msg("gateways.migrateBridgesBus")}</button></div>` +
    (busMigrateMsg ? note(esc(busMigrateMsg)) : ""));
  const list = buses.length
    ? buses.map(busCard).join("")
    : note(msg("gateways.noBusesYetBus"));
  const create = `<div class="row"><button type="button" class="btn" id="bus-create">${msg("gateways.createBus")}</button></div>`;
  return `<div class="stack">${peersCard()}${migrate}${list}${create}</div>`;
}

function busCard(bus) {
  const atts = (edit.attachments || []).filter((a) => a.bus_id === bus.id);
  const remotes = (edit.remote_attachments || []).filter((r) => r.bus_id === bus.id);
  const busy = busBusy[bus.name] || busBusy[bus.id];
  const busyBadge = busy
    ? `<span class="pill busy" title="Another source is talking; ${esc(busy.loser)} traffic is held off">busy: via ${esc(busy.winner)}${busy.node ? " @ " + esc(busy.node) : ""}</span>` : "";
  const enPill = `<button type="button" class="pill ${bus.enabled ? "on" : "off"}" data-busen="${esc(bus.id)}" aria-pressed="${bus.enabled}" aria-label="Bus enabled">${bus.enabled ? "ENABLED" : "DISABLED"}</button>`;
  const del = (atts.length === 0 && remotes.length === 0) ? `<button type="button" class="btn danger" data-busdel="${esc(bus.id)}">${msg("busCard.delete")}</button>` : "";
  const head = `<div class="card-head"><span class="sq"></span><span class="t">${esc(bus.name || bus.id)}</span>${busyBadge}<span class="bus-actions">${enPill}${del}</span></div>`;
  const nameRow = row(msg("busCard.name"), `<input data-busname="${esc(bus.id)}" value="${esc(bus.name)}" placeholder="e.g. Local Bus A">`);
  // Owner-offline state on a member (RFC-0016 §4), self-clearing (no latch).
  const down = busDown[bus.name] || busDown[bus.id];
  const downNote = down ? `<div class="note bus-down"><b>Bus ${esc(bus.name || bus.id)} down</b> — owner ${esc(down)} offline</div>` : "";
  const disableNote = bus.enabled ? "" : note(msg("busCard.disabledItsAttachmentsAre"));
  const total = atts.length + remotes.length;
  const lowNote = (bus.enabled && total < 2) ? note(msg("busCard.busNeedsLeastTwo")) : "";
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
    ? `<span class="pill off" title="peer not paired — this edge is dormant until re-paired">${msg("remoteAttachmentBlock.dormant")}</span>`
    : `<span class="pill on">${msg("remoteAttachmentBlock.viaPeer")}</span>`;
  const key = `${r.bus_id}|${r.peer_id}|${r.mode}`;
  return `<div class="attach remote-attach"><div class="toggle-row"><span class="name">${esc(BUS_MODE_LABEL[r.mode] || r.mode)} @ ${esc(pname)} ${badge}</span><button type="button" class="btn" data-remotedel="${esc(key)}">${msg("remoteAttachmentBlock.detach")}</button></div></div>`;
}

function attachmentBlock(a, idx) {
  const label = esc(BUS_MODE_LABEL[a.mode] || a.mode);
  const headRow = `<div class="toggle-row"><span class="name">${label} attachment</span><button type="button" class="btn" data-attachdel="${idx}">${msg("attachmentBlock.detach")}</button></div>`;
  // RFC-0003 Addendum A §2/§3: a YSF or NXDN attachment DISPLACES the stock gateway
  // — the bus becomes the mode's gateway on its loopback and reflectors are
  // unavailable for the duration. Say so in plain copy so it is a knowing choice,
  // never a silent side effect. DMR multiplexes onto DMRGateway and displaces
  // nothing, so it carries no such notice.
  const displaceNote = (a.mode === "ysf" || a.mode === "nxdn")
    ? `<div class="note bus-down"><b>While ${label} is attached to this bus, ${label} traffic goes to the bus, not to reflectors.</b> The stock ${label} gateway is stopped for the duration; detach to restore reflector access.</div>`
    : "";
  return `<div class="attach">${headRow}${displaceNote}${attachParams(a, idx)}</div>`;
}

function attachParams(a, idx) {
  if (a.mode === "dmr") {
    const slot = a.slot || "2";
    const slotSel = row(msg("attachParams.slot"), `<select data-attach="${idx}" data-akey="slot"><option value="1"${slot === "1" ? " selected" : ""}>1</option><option value="2"${slot === "2" ? " selected" : ""}>2</option></select>`);
    const nets = edit.networks || [];
    const creds = row(msg("attachParams.credentialsDmrNetwork"),
      `<select data-attach="${idx}" data-akey="credentials_ref"><option value="">${msg("attachParams.noneRidesLocalDmrgateway")}</option>${nets.map((n) => `<option value="${esc(n.name)}"${a.credentials_ref === n.name ? " selected" : ""}>${esc(n.name)}</option>`).join("")}</select>`);
    return slotSel + attField(idx, "default_tg", msg("attachParams.defaultTg"), "e.g. 91") + creds + tgMapEditor(a, idx);
  }
  if (a.mode === "ysf") {
    const opts = ysfRefs.map((r) => `<option value="${esc(r.name)}">${esc([r.country, r.description].filter(Boolean).join(" · "))}</option>`).join("");
    const target = row(msg("attachParams.reflectorDgId"), `<input data-attach="${idx}" data-akey="target" list="bus-ysf-refs" value="${esc(a.target || "")}" placeholder="e.g. FCS00290 or a YSF reflector"><datalist id="bus-ysf-refs">${opts}</datalist>`);
    const wx = `<div class="toggle-row"><span class="name">${msg("attachParams.wiresXPassthrough")}</span><button type="button" class="pill ${a.wiresx_passthrough ? "on" : "off"}" data-attachbool="${idx}" data-abkey="wiresx_passthrough" aria-pressed="${a.wiresx_passthrough}" aria-label="Wires-X passthrough">${a.wiresx_passthrough ? "ON" : "OFF"}</button></div>`;
    return target + wx;
  }
  if (a.mode === "nxdn") {
    return attField(idx, "id", msg("attachParams.nxdnId"), msg("attachParams.networkId")) + attField(idx, "tg", "TG", "talkgroup") + attField(idx, "default_id", msg("attachParams.defaultId"), "");
  }
  return note(msg("attachParams.modeCannotAttachCommitted"));
}

function attField(idx, key, label, ph) {
  const v = (edit.attachments[idx] || {})[key] || "";
  return row(label, `<input data-attach="${idx}" data-akey="${esc(key)}" value="${esc(v)}" placeholder="${esc(ph || "")}">`);
}

function tgMapEditor(a, idx) {
  const rows = (a._tgrows || []).map((r, ri) =>
    `<div class="row tgmap-row"><input data-tgmap="${idx}" data-tgi="${ri}" data-tgk="from" value="${esc(r.from)}" placeholder="source TG"><span class="arrow">→</span><input data-tgmap="${idx}" data-tgi="${ri}" data-tgk="to" value="${esc(r.to)}" placeholder="DMR TG"><button type="button" class="btn" data-tgdel="${idx}" data-tgi="${ri}" aria-label="Remove mapping">✕</button></div>`).join("");
  return `<div class="note">${msg("tgMapEditor.tgMapRewriteSource")}</div>${rows}<div class="row"><button type="button" class="btn" data-tgadd="${idx}">${msg("tgMapEditor.addMapping")}</button></div>`;
}

function attachPickerHTML(busId) {
  if (!attachPicker || attachPicker.busId !== busId) {
    return `<div class="row"><button type="button" class="btn" data-attachopen="${esc(busId)}">${msg("attachPickerHTML.attachMode")}</button></div>`;
  }
  if (attachPicker.loading) return note(msg("attachPickerHTML.checkingWhichModesCan"));
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
    peerSection = `<div class="note">${msg("attachPickerHTML.attachModePairedPeer")}</div><div class="picker-row">${peerBtns}</div>`;
    const rp = attachPicker.remote;
    if (rp) {
      const rbtns = rp.loading ? "Checking…" : (rp.opts || []).map((o) =>
        o.ok
          ? `<button type="button" class="btn attach-ok" data-attachrpick="${esc(rp.peerId)}|${esc(o.key)}">${esc(o.label)}</button>`
          : `<button type="button" class="btn attach-no" disabled title="${esc(o.reason)}">${esc(o.label)} — ${esc(o.reason)}</button>`).join("");
      peerSection += `<div class="note">via ${esc(peerName(rp.peerId))}:</div><div class="picker-row">${rbtns}</div>`;
    }
  }
  return `<div class="attach-picker"><div class="note">${msg("attachPickerHTML.attachLocalModeGreyed")}</div><div class="picker-row">${btns}</div>${peerSection}<div class="row"><button type="button" class="btn" data-attachcancel="1">${msg("attachPickerHTML.cancel")}</button></div></div>`;
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
    ? paired.map((p) => `<div class="toggle-row peer-row"><span class="name">${esc(p.name || p.id)}<span class="peer-fp" title="certificate fingerprint">${esc(shortFp(p.fingerprint))}</span></span><span class="bus-actions"><span class="pill on">${msg("peersCard.paired")}</span><button type="button" class="btn danger" data-peerrevoke="${esc(p.id)}" data-peername="${esc(p.name || p.id)}">${msg("peersCard.revoke")}</button></span></div>`).join("")
    : note(msg("peersCard.noPairedPeersYet"));
  const otherRows = other.map((p) => `<div class="toggle-row peer-row muted"><span class="name">${esc(p.name || p.id)}<span class="peer-fp">${esc(shortFp(p.fingerprint))}</span></span><span class="pill off">${esc((p.state || "").toUpperCase())}</span></div>`).join("");

  let disc = "";
  if (peering.discovered !== null) {
    disc = peering.discovered.length
      ? peering.discovered.map((d) => `<div class="row peer-disc"><label>${esc(d.instance || d.host)}<span class="peer-fp">${esc(d.host)}:${esc(String(d.port))}</span></label><button type="button" class="btn accent" data-peerpair="${esc(d.host)}:${esc(String(d.port))}">${msg("peersCard.pair")}</button></div>`).join("")
      : note(msg("peersCard.noPeersFoundLan"));
  }
  const discBtn = `<button type="button" class="btn" id="peer-discover"${peering.busy ? " disabled" : ""}>${peering.busy ? "Scanning…" : "Discover peers (mDNS)"}</button>`;
  const manual = `<div class="row"><input id="peer-manual" placeholder="host:port — e.g. 10.0.0.20:42501" aria-label="peer host and port"><button type="button" class="btn" id="peer-pair-manual">${msg("peersCard.pair")}</button></div>`;

  return card(msg("peersCard.lanPeersRfc0016"),
    note(msg("peersCard.pairWaypointNodesLan")) +
    `<div class="row">${discBtn}</div>` + disc + manual +
    (peering.msg ? note(esc(peering.msg)) : "") +
    `<div class="note">${msg("peersCard.pairedPeers")}</div>` + pairedRows + otherRows);
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
    : `<div class="pair-fp muted">${msg("peerModal.exchangingCertificate")}</div>`;
  let body;
  if (s.role === "initiator") {
    body = `<p>${msg("peerModal.enterCode")} <b>${esc(peer)}</b>${msg("peerModal.sLanPeersScreen")}</p>
      <div class="pair-code"><span class="mono" id="pair-code-val">${esc(s.code || "")}</span><button type="button" class="btn" data-paircopy="${esc(s.code || "")}" aria-label="copy code">${msg("peerModal.copy")}</button></div>
      ${fpRow}
      <div class="row pair-actions"><button type="button" class="btn accent" data-pairconfirm="${esc(s.sid)}">${msg("peerModal.confirmPairing")}</button><button type="button" class="btn" data-paircancel="${esc(s.sid)}">${msg("peerModal.cancel")}</button></div>`;
  } else {
    body = `<p>${msg("peerModal.incomingPairing")} <b>${esc(peer)}</b>. Enter the code shown on <b>${esc(peer)}</b>:</p>
      <div class="pair-code"><input class="mono pair-input" id="pair-code-input" inputmode="numeric" maxlength="6" placeholder="000000" aria-label="pairing code"></div>
      ${fpRow}
      <div class="row pair-actions"><button type="button" class="btn accent" data-pairenter="${esc(s.sid)}">${msg("peerModal.confirm")}</button><button type="button" class="btn" data-paircancel="${esc(s.sid)}">${msg("peerModal.cancel")}</button></div>`;
  }
  root.innerHTML = `<div class="pair-backdrop"><div class="pair-modal card"><div class="card-head"><span class="sq"></span><span class="t">${msg("peerModal.pairing")}</span></div>${body}</div></div>`;
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
  const supply = hostlistNote("ysf_hosts");
  // Startup reflector picker: a datalist over the fetched hostlist so the user
  // can type-filter YSF reflectors / FCS rooms while still allowing a raw id.
  const opts = ysfRefs.map((r) => `<option value="${esc(r.name)}">${esc([r.country, r.description].filter(Boolean).join(" · "))}</option>`).join("");
  const startup = (edit.ysfgw || {}).startup || "";
  const gateway = card(msg("common.gateway"),
    toggle("modes", "ysf", msg("ysf.systemFusion"), msg("common.enabled"), msg("common.disabled")) +
    input("ysfgw", "suffix", { label: "Suffix (RPT/ND)" }) +
    row(msg("common.startupReflector"), `<input data-sec="ysfgw" data-key="startup" list="ysf-refs" value="${esc(startup)}" placeholder="e.g. FCS00290 or a YSF reflector"><datalist id="ysf-refs">${opts}</datalist>`) +
    input("ysfgw", "inactivity_timeout", { label: "Inactivity revert", unit: "min" }));
  // Mode params render into MMDVM-Host's [System Fusion] (self_only, low_deviation,
  // remote_gateway, tx_hang, mode_hang) — the "ysf" store section, split from the
  // "ysfgw" gateway section like p25/p25gw and nxdn/nxdngw.
  const behaviour = card(msg("common.behaviour"),
    toggleRow("ysf", "self_only", msg("common.selfOnlyAcceptOnly")) +
    toggleRow("ysf", "low_deviation", msg("ysf.lowDeviationNarrowBand")) +
    toggleRow("ysf", "remote_gateway", msg("common.remoteGatewayAdvancedLeave")) +
    toggleRow("ysfgw", "wiresx_passthrough", msg("ysf.wiresXPassthroughAdvanced")) +
    toggleRow("ysfgw", "revert", msg("ysf.revertStartupInactivity")));
  const timers = card(msg("common.hangTimers"),
    input("ysf", "tx_hang", { label: "TX hang", unit: "sec" }) +
    input("ysf", "mode_hang", { label: "Mode hang", unit: "sec" }));
  const networks = card(msg("ysf.reflectorNetworks"),
    toggleRow("ysfgw", "ysf_network", msg("ysf.ysfReflectorNetwork")) +
    toggleRow("ysfgw", "fcs_network", msg("ysf.fcsRoomNetwork")) +
    toggleRow("ysfgw", "aprs", msg("ysf.aprsPositionBeacon")));
  // DG-ID gateway: swaps YSFGateway for DGIdGateway (mutually exclusive daemons).
  // With it on, the startup reflector links via a DG-ID (YCS network) and the
  // radio's Wires-X gateway sits on DG-ID 0.
  const dgid = card(msg("ysf.dgIdGateway"),
    toggleRow("ysfgw", "enable_dgid", msg("ysf.useDgidgatewayDgId")) +
    toggleRow("ysfgw", "ycs_network", msg("ysf.linkStartupReflectorDg")) +
    toggleRow("ysfgw", "upper_hostfiles", msg("ysf.uppercaseReflectorNamesHostlist")));
  const hint = ysfRefs.length ? "" : note(msg("ysf.reflectorListNotLoaded"));
  return `${supply}<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}${networks}${dgid}</div></div>${hint}`;
}

function panelP25() {
  const supply = hostlistNote("p25_hosts");
  // Startup-TG picker: a datalist over the fetched talkgroup list. Static is a
  // comma-separated list, so the datalist is a reference the user types from.
  const opts = p25Refs.map((r) => `<option value="${esc(r.designator)}">${esc([r.name, r.country, r.sponsor].filter(Boolean).join(" · "))}</option>`).join("");
  const stat = (edit.p25gw || {}).static || "";
  const gateway = card(msg("common.gateway"),
    toggle("modes", "p25", "P25", msg("common.enabled"), msg("common.disabled")) +
    input("p25", "nac", { label: "NAC (hex)", accent: true }) +
    row(msg("p25.startupTalkgroups"), `<input data-sec="p25gw" data-key="static" list="p25-refs" value="${esc(stat)}" placeholder="comma-separated TGs, e.g. 10100,10200"><datalist id="p25-refs">${opts}</datalist>`) +
    toggleRow("p25gw", "voice", msg("common.voiceAnnouncements")));
  const behaviour = card(msg("common.behaviour"),
    toggleRow("p25", "self_only", msg("p25.selfOnlyAcceptOnly")) +
    toggleRow("p25", "override_uid_check", msg("p25.overrideUidCheck")) +
    toggleRow("p25", "remote_gateway", msg("common.remoteGatewayAdvancedLeave")));
  const timers = card(msg("common.hangTimers"),
    input("p25gw", "rf_hang_time", { label: "RF hang", unit: "sec" }) +
    input("p25gw", "net_hang_time", { label: "Network hang", unit: "sec" }));
  const hint = p25Refs.length ? "" : note(msg("p25.talkgroupListNotLoaded"));
  return `${supply}<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}</div></div>${hint}`;
}

function panelNXDN() {
  const supply = hostlistNote("nxdn_hosts");
  // Startup-TG picker: a datalist over the fetched talkgroup list. Static is a
  // comma-separated list, so the datalist is a reference the user types from.
  const opts = nxdnRefs.map((r) => `<option value="${esc(r.designator)}">${esc([r.name, r.country, r.sponsor].filter(Boolean).join(" · "))}</option>`).join("");
  const stat = (edit.nxdngw || {}).static || "";
  const gateway = card(msg("common.gateway"),
    toggle("modes", "nxdn", "NXDN", msg("common.enabled"), msg("common.disabled")) +
    input("nxdn", "ran", { label: "RAN", accent: true }) +
    row(msg("nxdn.startupTalkgroups"), `<input data-sec="nxdngw" data-key="static" list="nxdn-refs" value="${esc(stat)}" placeholder="comma-separated TGs, e.g. 10200,65000"><datalist id="nxdn-refs">${opts}</datalist>`) +
    toggleRow("nxdngw", "voice", msg("common.voiceAnnouncements")));
  const behaviour = card(msg("common.behaviour"),
    toggleRow("nxdn", "self_only", msg("nxdn.selfOnlyAcceptOnly")) +
    toggleRow("nxdn", "remote_gateway", msg("common.remoteGatewayAdvancedLeave")));
  const timers = card(msg("common.hangTimers"),
    input("nxdngw", "rf_hang_time", { label: "RF hang", unit: "sec" }) +
    input("nxdngw", "net_hang_time", { label: "Network hang", unit: "sec" }));
  const hint = nxdnRefs.length ? "" : note(msg("nxdn.talkgroupListNotLoaded"));
  return `${supply}<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}</div></div>${hint}`;
}

function panelDStar() {
  const supply = hostlistNote("dstar_hosts");
  // Startup reflector picker: a datalist over the fetched hostlist so the user
  // can type-filter reflectors (REF/XRF/DCS) while still allowing a raw value.
  // The gateway wants "name module", e.g. "REF001 C", so the datalist offers
  // names the user completes with a band letter.
  const opts = dstarRefs.map((r) => `<option value="${esc(r.name)} ">${esc(r.type)}</option>`).join("");
  const gw = edit.dstargw || {};
  const reflector = gw.reflector || "";
  const gateway = card(msg("common.gateway"),
    toggle("modes", "dstar", "D-Star", msg("common.enabled"), msg("common.disabled")) +
    input("dstar", "module", { label: "Module (band letter)", accent: true }) +
    row(msg("common.startupReflector"), `<input data-sec="dstargw" data-key="reflector" list="dstar-refs" value="${esc(reflector)}" placeholder="e.g. REF001 C — blank for none"><datalist id="dstar-refs">${opts}</datalist>`) +
    input("dstargw", "reflector_reconnect", { label: "Reflector reconnect (min / Never / Fixed)" }));
  const ircddb = card(msg("dstar.ircddbCallsignRouting"),
    input("dstargw", "ircddb_hostname", { label: "ircDDB host" }) +
    input("dstargw", "ircddb_username", { label: "Username (blank = callsign)" }) +
    row(msg("dstar.password"), `<input data-sec="dstargw" data-key="ircddb_password" type="password" value="${esc(gw.ircddb_password || "")}" placeholder="${gw.has_ircddb_password ? "•••••• unchanged" : "blank = anonymous"}">`));
  const behaviour = card(msg("dstar.rfBehaviour"),
    toggleRow("dstar", "self_only", msg("common.selfOnlyAcceptOnly")) +
    toggleRow("dstar", "remote_gateway", msg("common.remoteGatewayAdvancedLeave")));
  const protocols = card(msg("dstar.reflectorProtocols"),
    toggleRow("dstargw", "dextra", msg("dstar.dextraXrf")) +
    toggleRow("dstargw", "dplus", msg("dstar.dPlusRefNeeds")) +
    row(msg("dstar.dPlusLogin"), `<input data-sec="dstargw" data-key="dplus_login" value="${esc(gw.dplus_login || "")}" placeholder="registered callsign (blank = station callsign)">`) +
    toggleRow("dstargw", "dcs", "DCS") +
    toggleRow("dstargw", "xlx", "XLX"));
  const hint = dstarRefs.length ? "" : note(msg("dstar.reflectorListNotLoaded"));
  return `${supply}<div class="grid2"><div class="stack">${gateway}${ircddb}</div><div class="stack">${behaviour}${protocols}</div></div>${hint}`;
}

function panelM17() {
  const supply = hostlistNote("m17_hosts");
  const opts = m17Refs.map((r) => `<option value="${esc(r.name)} ">${esc(r.address)}</option>`).join("");
  const gw = edit.m17gw || {};
  const suffix = (gw.suffix || "H").toUpperCase();
  const suffixSel = `<select data-sec="m17gw" data-key="suffix">` +
    ["H", "R"].map((v) => `<option value="${v}"${v === suffix ? " selected" : ""}>${v === "H" ? "H — hotspot" : "R — repeater"}</option>`).join("") + `</select>`;
  const gateway = card(msg("common.gateway"),
    toggle("modes", "m17", "M17", msg("common.enabled"), msg("common.disabled")) +
    input("m17", "can", { label: "CAN", accent: true }) +
    row(msg("common.startupReflector"), `<input data-sec="m17gw" data-key="startup" list="m17-refs" value="${esc(gw.startup || "")}" placeholder="e.g. M17-M17 C — blank for none"><datalist id="m17-refs">${opts}</datalist>`) +
    row(msg("m17.nodeSuffix"), suffixSel) +
    toggleRow("m17gw", "voice", msg("common.voiceAnnouncements")));
  const behaviour = card(msg("common.behaviour"),
    toggleRow("m17", "self_only", msg("common.selfOnlyAcceptOnly")) +
    toggleRow("m17", "allow_encryption", msg("m17.allowEncryptedM17Frames")) +
    toggleRow("m17gw", "revert", msg("m17.revertStartupReflectorAfter")));
  const timers = card(msg("m17.hangTimer"),
    input("m17gw", "hang_time", { label: "Network hang", unit: "sec" }));
  const hint = m17Refs.length ? "" : note(msg("m17.reflectorListNotLoaded"));
  return `${supply}<div class="grid2">${gateway}<div class="stack">${behaviour}${timers}</div></div>${hint}`;
}

// The POCSAG panel splits into the "modes" enable + the "pocsag" store section
// (paging frequency + DAPNETGateway login/filters). The AuthKey is a redacted
// secret: it starts blank (blank = keep the stored one) and has_auth_key drives
// the placeholder, exactly like the ircDDB password.
function panelPocsag() {
  const p = edit.pocsag || (edit.pocsag = {});
  const paging = card(msg("pocsag.pagingChannel"),
    toggle("modes", "pocsag", "POCSAG", msg("common.enabled"), msg("common.disabled")) +
    input("pocsag", "frequency", { label: "Paging frequency", kind: "mhz", unit: "MHz", accent: true }));
  const dapnet = card(msg("pocsag.dapnetLogin"),
    input("pocsag", "server", { label: "DAPNET server" }) +
    input("pocsag", "callsign", { label: "Callsign (blank = station callsign)" }) +
    row(msg("pocsag.authkey"), `<input data-sec="pocsag" data-key="auth_key" type="password" value="${esc(p.auth_key || "")}" placeholder="${p.has_auth_key ? "•••••• unchanged" : "from the DAPNET portal"}">`));
  const filters = card(msg("pocsag.ricFiltersOptional"),
    input("pocsag", "whitelist", { label: "Whitelist (comma-separated RICs)" }) +
    input("pocsag", "blacklist", { label: "Blacklist (comma-separated RICs)" }));
  const hint = note(msg("pocsag.dapnetgatewayWillNotConnect"));
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
  const access = card(msg("fm.access"),
    toggle("modes", "fm", "FM", msg("common.enabled"), msg("common.disabled")) +
    input("fm", "ctcss", { label: "CTCSS tone", unit: "Hz", accent: true }) +
    row(msg("fm.accessMode"), amSel));
  const timing = card(msg("fm.timing"),
    input("fm", "timeout", { label: "Timeout", unit: "sec" }) +
    input("fm", "kerchunk_time", { label: "Kerchunk time", unit: "sec" }));
  const audio = card(msg("fm.audioLevels"),
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
  const retention = card(msg("station.eventHistory"),
    row(msg("station.retentionWindow"),
      `<div class="unit"><input data-sec="history" data-key="retention_days" data-kind="int" inputmode="numeric" value="${esc(days)}"><span class="u">days</span></div>`) +
    note(msg("station.howLongNodeKeeps")));
  // Automatic CW identification. The effective callsign is echoed back from the
  // view rather than computed here, so what the operator reads is exactly what the
  // renderer will put in [CW Id].
  const sid = edit.station_id || (edit.station_id = { enable: true, time_mins: "10", callsign: "", tx_level: "50" });
  // effective_callsign is server-derived, so it lags an unsaved callsign edit —
  // fall back to the working copy so the preview tracks what the operator just typed.
  const effective = esc(sid.callsign.trim() || (edit.general || {}).callsign || ((state.config || {}).station_id || {}).effective_callsign || "");
  const idRows =
    toggle("station_id", "enable", msg("station.identifyAutomatically"), "ON", "OFF") +
    row(msg("station.interval"),
      `<div class="unit"><input data-sec="station_id" data-key="time_mins" inputmode="numeric" value="${esc(sid.time_mins)}"><span class="u">minutes</span></div>`) +
    input("station_id", "callsign", { label: "Callsign override" }) +
    row(msg("station.toneLevel"),
      `<div class="unit"><input data-sec="station_id" data-key="tx_level" inputmode="numeric" value="${esc(sid.tx_level)}"><span class="u">%</span></div>`);
  const idNote = sid.enable
    ? note(`Your node keys <b>${effective}</b> ${msg("station.morseEvery")} <b>${esc(sid.time_mins)}</b> ${msg("station.minuteSWhileIdle")} <b>${msg("station.mostLicencesRequirePeriodic")}</b> — in the US, every 10 minutes (§97.119).`)
    : note(msg("station.nodeWillNotIdentify"));
  // The host only keys CW between transmissions, so a long transmission is not
  // identified until after it ends. Say so here rather than let an operator infer a
  // guarantee the software cannot make.
  const idCaveat = note(msg("station.identificationSentBetweenTransmissions"));
  const beacon = card(msg("station.stationIdentification"), idRows + idNote + idCaveat);
  return `<div class="grid2">${retention}${beacon}</div>`;
}

// panelUpdates renders the Software Updates tab (RFC-0014): installed versions,
// available stack updates with an apply/check control, the update policy (channel,
// auto-apply, quiet window — saved through the normal Apply flow), and recent
// update history. Pure: state + stackStatus + edit in, HTML string out.
function panelUpdates() {
  const st = stackStatus || {};
  const wv = (state.health && state.health.version) || "—";
  const inst = st.installed || {};
  const shortName = (p) => p.replace(/^waypoint-/, "");
  const daemons = ["waypoint-mmdvmhost", "waypoint-dmrgateway", "waypoint-ysfgateway", "waypoint-p25gateway", "waypoint-nxdngateway", "waypoint-dstargateway", "waypoint-m17gateway"];

  // Installed versions.
  let verRows = row("waypointd", `<span class="accent">${esc(wv)}</span>`);
  if (st.configured) {
    verRows += row(msg("updates.stack"), `<span class="accent">${esc(inst["waypoint-stack"] || "—")}</span>`);
    verRows += daemons.map((p) => row(shortName(p), `<span>${esc(inst[p] || "—")}</span>`)).join("");
  }
  const versions = card(msg("updates.installedVersions"), verRows);

  // A new waypointd release, from the cached signed-manifest check (#15). Shown
  // above the stack rows because it is the node's own binary, not a service.
  const bin = st.binary || {};
  let binInner = "";
  if (bin.available) {
    binInner = row("waypointd", `<span>${esc(bin.current || wv)} → <span class="accent">${esc(bin.version || "—")}</span></span>`) +
      (bin.notes_url ? row(msg("updates.releaseNotes"), extLink(bin.notes_url, msg("updates.whatChanged"))) : "") +
      note(msg("updates.newWaypointdReleaseAvailable"));
  }

  // Available updates + apply/check.
  let availInner;
  if (!st.configured) {
    availInner = note(msg("updates.signedAptRepoNot"));
  } else if (st.applying) {
    availInner = note(msg("updates.applyingUpdateServicesAre"));
  } else if ((st.available || []).length) {
    const list = (st.available || []).map((u) =>
      row(shortName(u.package), `<span>${esc(u.from || "—")} → <span class="accent">${esc(u.to || "—")}</span></span>`)).join("");
    availInner = list +
      `<div class="row"><label></label><button type="button" id="stack-apply" class="btn primary">${msg("updates.updateNow")}</button></div>` +
      note(msg("updates.applyingStopsAffectedServices"));
  } else {
    availInner = note(hasCheck(st) ? `Up to date. Last checked ${fmtWhen(st.last_check)}.` : "No update check has run yet.");
  }
  if (!bin.available && hasStamp(st.last_binary_check)) {
    availInner += note(`waypointd is up to date. Last checked ${fmtWhen(st.last_binary_check)}.`);
  }
  // CHECK NOW is offered even without the apt repo: there is always the waypointd
  // manifest to ask about, and asking is the operator's own action.
  availInner += `<div class="row"><label></label><button type="button" id="stack-check" class="btn ghost">${msg("updates.checkNow")}</button></div>`;
  if (st.configured && st.last_result) availInner += note(`Last result: ${esc(st.last_result)}`);
  // Say so when the node is not checking on its own, so "no update check has run
  // yet" reads as a setting rather than a fault.
  if ((st.prefs || {}).check_enabled === false) {
    availInner += note(msg("updates.automaticUpdateChecksAre"));
  }
  const available = card(msg("updates.availableUpdates"), binInner + availInner);

  // Update policy (edit-backed; saved with Apply Changes).
  const u = edit.update || (edit.update = { channel: "stable", check_enabled: true, auto_apply: false, quiet_window: "04:00" });
  const chan = u.channel || "stable";
  const chanSel = `<select data-sec="update" data-key="channel">` +
    [["stable", "Stable"], ["beta", "Beta"]].map(([v, l]) => `<option value="${v}"${v === chan ? " selected" : ""}>${esc(l)}</option>`).join("") +
    `</select>`;
  const policy = card(msg("updates.updatePolicy"),
    row(msg("updates.channel"), chanSel) +
    toggle("update", "check_enabled", msg("updates.automaticUpdateChecks"), "ON", "OFF") +
    toggle("update", "auto_apply", msg("updates.automaticUpdates"), "ON", "OFF") +
    row(msg("updates.quietWindow"), `<input type="time" data-sec="update" data-key="quiet_window" value="${esc(u.quiet_window || "04:00")}">`) +
    note(msg("updates.automaticUpdateChecksAsk")) +
    note(msg("updates.defaultNotifyClickUpdates")));

  // Recent history.
  const hist = (st.history || []).slice(0, 8);
  const histInner = hist.length
    ? hist.map((h) => row(shortName(h.package), `<span>${esc(h.from || "—")} → ${esc(h.to || "—")} · ${esc(h.result)}</span>`)).join("")
    : note(msg("updates.noStackUpdateHistory"));
  const history = card(msg("updates.recentUpdates"), histInner);

  return `<div class="grid2">${versions}${available}</div><div class="grid2">${policy}${history}</div>`;
}


// --- Hardware: modem identity, detection, and the GPIO UART (#18) ---------
//
// The tab answers three questions in the order an operator asks them:
//
//   1. Is there a modem, and what is it? (detection, and what it said)
//   2. Is this node configured to match?  (the disagreements, if any)
//   3. If nothing was found, why not?     (the GPIO serial port's availability)
//
// Detect and Adopt are separate buttons because they are separate acts: one
// reads the world, the other changes the node. Adopt is also where the operator
// answers the question the wire cannot — several products ship the same firmware
// and report the same identity string, so the picker is narrowed to the ones the
// modem could actually be, never to a guess.
function panelHardware() {
  const hw = hardware || {};
  const det = hw.detected || {};
  const id = det.identity;
  const cfg = hw.configured || {};

  // --- what is attached ---
  let idInner;
  if (id) {
    const proto = id.protocol === 2 ? "2 (reports its own capabilities)" : "1 (capabilities assumed)";
    idInner =
      row(msg("hardware.identity"), `<span class="accent">${esc(id.description || id.hw_type || "—")}</span>`) +
      row(msg("hardware.found"), `<span>${esc(id.port)} · ${esc((id.transport || "").toUpperCase())} · ${esc(String(id.baud || ""))} baud</span>`) +
      (id.firmware ? row(msg("hardware.firmware"), `<span>${esc(id.firmware)}${id.built ? " (" + esc(id.built) + ")" : ""}${id.author ? " by " + esc(id.author) : ""}</span>`) : "") +
      row(msg("hardware.referenceOscillator"), `<span>${esc(tcxoText(id))}</span>`) +
      row(msg("hardware.radios"), `<span>${id.duplex ? "Two (duplex-capable)" : "One (simplex)"}</span>`) +
      row(msg("hardware.protocol"), `<span>${esc(proto)}</span>`) +
      (id.udid ? row(msg("hardware.chipId"), `<span class="mono-sm">${esc(id.udid)}</span>`) : "") +
      row(msg("hardware.modesFirmwareCarries"), `<span>${esc(modeSupportText(id.modes))}</span>`);
    if (det.checked_at) idInner += note(`Last detected ${esc(fmtWhen(det.checked_at))}.`);
  } else if (det.checked_at) {
    idInner = note(`<b>${msg("hardware.noModemAnswered")}</b> Every candidate port was asked and none replied — the table below shows which, and what each one did. Last looked ${esc(fmtWhen(det.checked_at))}.`);
  } else {
    idInner = note(msg("hardware.nodeHasNotLooked"));
  }
  if (det.bootloader) {
    idInner += note(`<b>${msg("hardware.boardSittingItsBootloader")}</b> on ${esc(det.bootloader)}. It cannot answer a version request in that state, but it is one firmware flash away from working.`);
  }
  idInner += `<div class="row"><label></label><button type="button" id="hw-detect" class="btn primary"${hwBusy ? " disabled" : ""}>${hwBusy ? "DETECTING…" : "DETECT"}</button></div>`;
  const identity = card(msg("hardware.attachedModem"), idInner);

  // --- adopt into the config ---
  let adoptInner = "";
  if (id) {
    const cands = id.candidates || [];
    const list = cands.length ? cands : (hw.boards || []).map((b) => b.id);
    const byID = {};
    (hw.boards || []).forEach((b) => { byID[b.id] = b; });
    const chosen = cands.includes(cfg.board) ? cfg.board : (id.board_id || cands[0] || "");
    const opts = list.map((bid) => {
      const b = byID[bid] || { id: bid, name: bid };
      return `<option value="${esc(b.id)}"${b.id === chosen ? " selected" : ""}>${esc(b.name)}</option>`;
    }).join("");
    adoptInner += row(msg("hardware.board"), `<select id="hw-board">${opts}</select>`);
    if (cands.length > 1) {
      adoptInner += note(`<b>${cands.length} boards ship this firmware</b>, and nothing the modem says tells them apart. Pick the one you have — the choice changes no generated config, it is what lets Waypoint refuse a setting your board cannot do.`);
    } else if (!cands.length) {
      adoptInner += note(msg("hardware.modemAnsweredButIts"));
    }
    adoptInner += `<div class="row"><label></label><button type="button" id="hw-adopt" class="btn accent"${hwBusy ? " disabled" : ""}>${msg("hardware.useModem")}</button></div>`;
    adoptInner += note(msg("hardware.adoptingWritesPortLine"));
  } else {
    adoptInner = note(msg("hardware.nothingAdoptUntilModem"));
  }
  // Two cards, not one: what this node currently believes, and the act of
  // changing that belief. Folding them together would put two controls labelled
  // "board" in one card — the read-only one and the picker — which is confusing
  // to read and ambiguous to a screen reader.
  const configured = card(msg("hardware.nodeConfigured"),
    row(msg("hardware.port"), `<span>${esc(cfg.port || "—")}</span>`) +
    row(msg("hardware.lineSpeed"), `<span>${esc(cfg.uart_speed || "115200 (default)")}</span>`) +
    row(msg("hardware.board2"), `<span>${esc(cfg.board_name || cfg.board || "—")}</span>`) +
    row(msg("hardware.referenceOscillator"), `<span>${esc(cfg.tcxo_label || cfg.tcxo_hz || "—")}</span>`) +
    (det.adopted_description ? note(`Taken from a detected modem identifying as <b>${esc(det.adopted_description)}</b>${det.adopted_at ? ", " + esc(fmtWhen(det.adopted_at)) : ""}.`) : ""));
  const adopt = card(msg("hardware.useDetectedModem"), adoptInner);

  // --- disagreements ---
  const warns = hw.warnings || [];
  let warnCard = "";
  if (warns.length) {
    const items = warns.map((w) => {
      const bad = w.severity === "error";
      return `<li class="hw-warn ${bad ? "bad" : "warn"}"><span class="hw-warn-k">${esc(bad ? "Will not work" : "Check this")}</span> <span class="hw-warn-f">${esc(w.field)}</span><div>${esc(w.message)}</div></li>`;
    }).join("");
    warnCard = card(msg("hardware.nodeModemDisagree"), `<ul class="hw-warns">${items}</ul>` +
      note(msg("hardware.theseAreConfigurationsProduce")));
  }

  // --- what was looked at ---
  let scanCard = "";
  const scanned = det.scanned || [];
  if (scanned.length) {
    const rows = scanned.map((sc) => {
      const what = sc.known ? esc(sc.known) : (sc.usb_id ? "USB " + esc(sc.usb_id) : esc((sc.transport || "").toUpperCase()));
      return row(sc.port, `<span>${esc(outcomeText(sc.outcome))} · ${what}${sc.detail ? " · " + esc(sc.detail) : ""}</span>`);
    }).join("");
    scanCard = card(msg("hardware.portsLooked"), rows +
      note(msg("hardware.portsAlreadyClaimedSomething")));
  }

  // --- the GPIO serial port ---
  let uartCard = "";
  const uart = hw.uart;
  if (uart) {
    if (uart.ok) {
      uartCard = card(msg("hardware.gpioSerialPort"), note(msg("hardware.gpioSerialPortFree")));
    } else {
      const probs = (uart.problems || []).map((p) => `<li>${esc(p.message)}</li>`).join("");
      uartCard = card(msg("hardware.gpioSerialPort"), 
        note(msg("hardware.hatFittedNodeCannot")) +
        `<ul class="hw-warns">${probs}</ul>` +
        `<div class="row"><label></label><button type="button" id="hw-uart" class="btn primary"${hwBusy ? " disabled" : ""}>${msg("hardware.freeSerialPort")}</button></div>` +
        note(msg("hardware.editsConfigTxtCmdline")));
    }
  }

  return `<div class="grid2">${identity}<div class="stack">${configured}${adopt}</div></div>${warnCard}${panelFirmware()}${panelCalibration()}${uartCard}${scanCard}`;
}

// --- Calibration: measuring the oscillator error (#20 / RFC-0021) ---------
//
// The panel is built around one idea: the operator is being shown a
// MEASUREMENT, and they decide whether to believe it. So the curve is on screen
// before any button writes anything, a candidate nothing was heard on is drawn
// differently from one that measured clean, and the offset the node is running
// now is always visible next to the one being offered.
//
// Everything that gates the sweep comes from the server's own reason string
// rather than being re-derived here. There are three preconditions (a modem, a
// frequency, and nothing else using the modem) and two answers to "why is this
// greyed out" would be one too many.
function panelCalibration() {
  const c = calib || {};
  const job = c.job;
  const running = !!(job && !job.ended);
  const last = c.last || {};
  const result = (job && job.result) || last.result || null;

  let inner = "";

  if (c.rx_freq_hz) {
    inner += row(msg("cal.listening"), `<span class="accent">${esc(mhz(c.rx_freq_hz))} MHz</span>${c.band ? ` <span>· ${esc(c.band)}</span>` : ""}`);
  }
  inner += row(msg("cal.offsetUseNow"), `<span>${esc(offsetText(c.current_rx_offset))} RX · ${esc(offsetText(c.current_tx_offset))} TX</span>`);
  if (last.ran_at && !running) {
    inner += row(msg("cal.lastSwept"), `<span>${esc(fmtWhen(last.ran_at))}${last.board_id ? " · " + esc(last.board_id) : ""}</span>`);
  }

  if (!c.available && c.reason) {
    inner += note(`<b>${msg("cal.calibrationCannotRunYet")}</b> ${esc(c.reason)}`);
  }

  // What the operator has to do, said before the button rather than after it.
  // A sweep with nobody transmitting measures nothing, and that is the single
  // most likely way a first attempt fails.
  if (c.available && !running) {
    inner += note("<b>You need your radio for this.</b> Set it to <b>DMR</b> on " +
      `<b>${esc(mhz(c.rx_freq_hz))} MHz</b>${msg("cal.colourCode")} <b>${esc(String(c.color_code || 1))}</b>, then start the sweep and hold PTT when it asks. ` +
      "It steps the modem's own frequency across a few kHz and scores each step by the bit error rate of what it hears. Let go whenever you like — it pauses and picks up where it left off.");
    if (c.host_running) {
      inner += note(msg("cal.nodeGoesOffAir"));
    }
  }

  if (running) inner += calProgress(job);
  if (job && job.error && !running) {
    inner += note(`<b>${msg("cal.sweepDidNotFinish")}</b> ${esc(job.error)}`);
  }

  if (result) inner += calCurve(result, c);

  const btn = running
    ? `<button type="button" id="cal-cancel" class="btn">STOP</button>`
    : `<button type="button" id="cal-sweep" class="btn primary"${c.available && !calBusy ? "" : " disabled"}>${calBusy ? "STARTING…" : "START SWEEP"}</button>`;
  let actions = `<div class="row"><label></label>${btn}`;
  if (!running && result && result.best) {
    actions += ` <button type="button" id="cal-apply" class="btn accent"${calBusy ? " disabled" : ""}>USE ${offsetText(String(result.best.offset_hz))}</button>`;
  }
  actions += `</div>`;
  inner += actions;

  if (!running && result && result.best) {
    inner += note(msg("cal.applyingWritesSameNumber"));
  }
  return card(msg("cal.calibration"), inner);
}

// calProgress is the live readout. It is not a percentage bar: a sweep that is
// waiting for the operator to key up has no meaningful percentage, and showing
// one stalled at 40% would read as a hang rather than as "your turn".
function calProgress(job) {
  const p = calLive || job;
  const waiting = p.phase === "waiting";
  const step = p.steps ? `${p.step} of ${p.steps}` : "";
  const berText = p.frames ? `${Number(p.ber_percent || 0).toFixed(3)}% over ${p.frames} frames` : "listening…";
  return `<div class="fw-prog">
    <div class="fw-prog-lab"><span>${esc(waiting ? "WAITING FOR YOUR RADIO" : "MEASURING " + offsetText(String(p.offset_hz || 0)))}</span><span>${esc(step)}</span></div>
    <div class="cal-live">${esc(waiting ? (p.detail || "key your radio and hold it") : berText)}</div>
  </div>`;
}

// calCurve draws the measurement. Points nothing was heard on are drawn as gaps
// rather than as zeroes, because 0.00% BER and "never decoded anything here" are
// the same number and opposite meanings — conflating them is exactly how a sweep
// would recommend a frequency the radio was never audible on.
function calCurve(res, c) {
  const pts = (res.points || []).filter((p) => p.heard);
  const silent = (res.points || []).filter((p) => !p.heard).length;
  if (!pts.length) {
    return note(msg("calCurve.nothingWasDecodedAny"));
  }
  const worst = Math.max(...pts.map((p) => p.ber_percent), 0.001);
  const bestOff = res.best ? res.best.offset_hz : null;
  const bars = (res.points || []).map((p) => {
    const h = p.heard ? Math.max(3, Math.round((p.ber_percent / worst) * 100)) : 0;
    const cls = !p.heard ? "silent" : (p.offset_hz === bestOff ? "best" : (p.scored ? "" : "partial"));
    const label = p.heard
      ? `${offsetText(String(p.offset_hz))}: ${p.ber_percent.toFixed(3)}% over ${p.frames} frames`
      : `${offsetText(String(p.offset_hz))}: nothing heard`;
    // title only, no per-bar aria-label: the bars live inside a role="img", so
    // their children are presentational to a screen reader anyway, and the
    // container's label below is what actually gets read out.
    return `<div class="cal-bar ${cls}" title="${esc(label)}"><i style="height:${h}%"></i></div>`;
  }).join("");

  // The chart's alternative text is the CONCLUSION, not a description of the
  // picture. Someone who cannot see the bars needs the same thing the bars are
  // there to convey: where the minimum is and how deep it is.
  const alt = res.best
    ? `Bit error rate against frequency offset. Best ${offsetText(String(res.best.offset_hz))} at ${res.best.ber_percent.toFixed(3)} percent, measured on ${res.best.frames} frames. ${pts.length} of ${res.points.length} frequencies decoded a signal.`
    : `Bit error rate against frequency offset. ${pts.length} of ${res.points.length} frequencies decoded a signal; none was measured on enough frames to choose.`;
  let out = `<div class="cal-chart" role="img" aria-label="${esc(alt)}">${bars}</div>`;
  out += `<div class="cal-axis"><span>${esc(offsetText(String((res.points[0] || {}).offset_hz || 0)))}</span><span>${msg("calCurve.lowerBetter")}</span><span>${esc(offsetText(String((res.points[res.points.length - 1] || {}).offset_hz || 0)))}</span></div>`;
  if (res.best) {
    out += row(msg("calCurve.best"), `<span class="accent">${esc(offsetText(String(res.best.offset_hz)))} at ${res.best.ber_percent.toFixed(3)}% BER</span>`);
  }
  if (silent) {
    out += note(`${silent} of ${res.points.length} frequencies decoded nothing at all — that is normal at the edges of the sweep, where the signal is too far off frequency for the modem to hear.`);
  }
  if (res.aborted) {
    out += note(msg("calCurve.curveIncompleteSweepRan"));
  }
  return out;
}

function offsetText(v) {
  const n = Number(v || 0);
  return (n > 0 ? "+" : "") + n + " Hz";
}

// --- Firmware: what is on the modem, and changing it (#19) ----------------
//
// Flashing is the operation operators are most afraid of, and the panel is
// written to defuse that rather than to look impressive. Three things are on
// screen before the button: which image would be written and why that one, that
// the node goes off the air while it happens, and — on the GPIO path — that an
// interrupted flash is recoverable by retry.
//
// The button is greyed with the SERVER's reason, never a locally-derived one.
// Which firmware fits a board depends on the oscillator, the radio count and the
// transport, and re-implementing that in JavaScript would give an operator two
// answers that can disagree. When the server refuses because the choice is
// ambiguous, its list of candidates becomes the picker.
function panelFirmware() {
  const fw = firmware || {};
  const job = fw.job;
  const running = !!(job && !job.ended);
  const id = (hardware && hardware.detected && hardware.detected.identity) || null;

  let inner = "";

  if (id) {
    inner += row(msg("firmware.runningNow"), `<span class="accent">${esc(id.firmware ? "v" + id.firmware : (id.description || "unknown"))}</span>`);
  }
  if (fw.catalog_version) {
    inner += row(msg("firmware.available"), `<span>${esc(fw.catalog_version)}</span>`);
  }

  // What would be written, or why nothing can be. The two refusals read
  // differently on purpose: one is a dead end, the other is a question. Saying
  // "nothing can be flashed" above an enabled button and a picker would be
  // telling the operator the opposite of what the screen is doing.
  const choices = fw.choices || [];
  if (fw.match) {
    inner += row(msg("firmware.wouldWrite"), `<span>${esc(fw.match.describe)}</span>`);
  } else if (fw.reason && choices.length) {
    inner += note(`<b>${msg("firmware.waypointWillNotChoose")}</b> ${esc(fw.reason)}`);
  } else if (fw.reason) {
    inner += note(`<b>${msg("firmware.nothingCanFlashedYet")}</b> ${esc(fw.reason)}`);
  }

  // The picker: only when the server said the choice is the operator's. Its
  // options are the server's candidates, so the operator can never pick an
  // image the daemon would refuse.
  if (choices.length) {
    const byID = {};
    (fw.variants || []).forEach((v) => { byID[v.id] = v; });
    const opts = choices.map((cid) => {
      const v = byID[cid] || { id: cid, describe: cid };
      return `<option value="${esc(v.id)}">${esc(v.describe || v.id)}</option>`;
    }).join("");
    inner += row(msg("firmware.firmwareWrite"), `<select id="fw-variant">${opts}</select>`);
    inner += note(msg("firmware.pickImageMatchesOscillator"));
  }

  if (running) {
    inner += fwProgressHTML(job);
  } else if (job && job.error) {
    inner += note(`<b>${msg("firmware.lastFlashFailed")}</b> ${esc(job.error)}<br>The modem was released and MMDVM-Host restarted. On a GPIO board the bootloader is in the chip itself, so a failed write leaves the board reachable exactly as it was — press FLASH again.`);
  } else if (job && job.after) {
    inner += note(`<b>${msg("firmware.flashed")}</b> ${esc(job.before ? "v" + job.before + " → v" + job.after : "v" + job.after)}${job.detail ? " (" + esc(job.detail) + ")" : ""}.`);
  } else if (job && job.variant) {
    inner += note(`<b>Flashed ${esc(job.detail || job.variant)}.</b> The modem did not answer a re-probe afterwards, so the new version could not be read back — the write itself was verified.`);
  }

  const canFlash = !!(fw.available && (fw.match || choices.length) && !running && !fwBusy);
  inner += `<div class="row"><label></label>` +
    `<button type="button" id="fw-flash" class="btn primary"${canFlash ? "" : " disabled"}>` +
    `${running ? "FLASHING…" : "FLASH FIRMWARE"}</button>` +
    `<button type="button" id="fw-refresh" class="btn"${fwBusy || running ? " disabled" : ""}>${msg("firmware.checkFirmware")}</button></div>`;

  if (fw.from_config) {
    inner += note(msg("firmware.nothingAnsweredDetectionSo"));
  }
  if (fw.host_running) {
    inner += note(msg("firmware.nodeAirFlashingStops"));
  }
  inner += note(msg("firmware.firmwareDownloadedSignedRelease"));

  return card(msg("firmware.modemFirmware"), inner);
}

// fwProgressHTML draws the running job. The stage and percentage are written out
// as text as well as drawn, because a bar on its own says nothing to a screen
// reader and very little to anyone watching a write that takes half a minute.
function fwProgressHTML(job) {
  const pct = job.total > 0 ? Math.round((job.done / job.total) * 100) : 0;
  const known = job.total > 0;
  const stage = fwStageText(job.stage);
  const lab = known ? stage + " · " + pct + "%" : stage;
  return `<div class="fw-prog">
    <div class="fw-prog-lab"><span>${esc(lab)}</span><span>${esc(job.detail || "")}</span></div>
    <div class="fw-bar${known ? "" : " indeterminate"}" role="progressbar"
         aria-label="Firmware flash progress"
         ${known ? `aria-valuenow="${pct}" aria-valuemin="0" aria-valuemax="100"` : ""}
         aria-valuetext="${esc(lab)}"><i style="width:${known ? pct : 40}%"></i></div>
  </div>`;
}

function fwStageText(st) {
  switch (st) {
    case "choosing": return "Choosing the firmware";
    case "fetching": return "Downloading and verifying";
    case "preparing": return "Stopping the modem and entering its bootloader";
    case "erasing": return "Erasing";
    case "writing": return "Writing";
    case "verifying": return "Reading back to verify";
    case "restarting": return "Restarting the modem";
    case "done": return "Finished";
    default: return st || "Working";
  }
}

// loadFirmware fetches the firmware surface. It is deliberately separate from
// loadHardware: the catalog is a network fetch that can be slow or fail, and a
// firmware release being unreachable must not stop the hardware tab rendering
// what is attached.
async function loadFirmware() {
  try {
    firmware = await fetch("/api/flash").then((r) => r.json());
  } catch {
    firmware = null;
  }
  if (firmware && firmware.job && !firmware.job.ended) watchFlash();
  if (state.tab === "hardware") renderPanel();
}

// watchFlash follows a running job over SSE. Byte-level progress is its own
// stream rather than the dashboard's event feed, because every event on that
// feed is written to the SD card and a progress bar is not worth five hundred
// rows (RFC-0019).
function watchFlash() {
  if (fwStream) return;
  try {
    fwStream = new EventSource("/api/flash/events");
  } catch {
    return;
  }
  fwStream.onmessage = (ev) => {
    let p;
    try { p = JSON.parse(ev.data); } catch { return; }
    if (!firmware) firmware = {};
    firmware.job = Object.assign({}, firmware.job, {
      stage: p.stage, done: p.done || 0, total: p.total || 0,
      detail: p.detail || (firmware.job && firmware.job.detail) || "",
    });
    if (state.tab === "hardware") renderPanel();
    if (p.stage === "done") stopWatchingFlash();
  };
  // A dropped stream is not a failed flash — the daemon runs the job on its own
  // deadline, not the browser's — so this re-reads the authoritative state
  // instead of reporting a failure the node has not had.
  fwStream.onerror = () => { stopWatchingFlash(); setTimeout(loadFirmware, 1500); };
}

function stopWatchingFlash() {
  if (!fwStream) return;
  fwStream.close();
  fwStream = null;
  // The stream carries progress, not outcomes: the job's result (the new version
  // string, or the error) comes from the panel endpoint.
  setTimeout(loadFirmware, 400);
}

// loadCalibration fetches the calibration surface. Like the firmware panel it is
// its own fetch: a node with no modem still renders everything else.
async function loadCalibration() {
  try {
    calib = await fetch("/api/cal").then((r) => r.json());
  } catch {
    calib = null;
  }
  if (calib && calib.job && !calib.job.ended) watchCal();
  if (state.tab === "hardware") renderPanel();
}

// watchCal follows a running sweep over SSE. Per-frame BER is its own stream and
// is never written to the event store — a sweep is several hundred updates, and
// an SD card is not the place to animate a chart (RFC-0019 §8).
function watchCal() {
  if (calStream) return;
  try {
    calStream = new EventSource("/api/cal/events");
  } catch {
    return;
  }
  calStream.onmessage = (ev) => {
    let p;
    try { p = JSON.parse(ev.data); } catch { return; }
    calLive = p;
    if (state.tab === "hardware") renderPanel();
    if (p.phase === "done") stopWatchingCal();
  };
  // A dropped stream is not a failed sweep: the daemon runs it on its own
  // deadline, holding the modem port, whatever the browser is doing.
  calStream.onerror = () => { stopWatchingCal(); setTimeout(loadCalibration, 1500); };
}

function stopWatchingCal() {
  if (!calStream) return;
  calStream.close();
  calStream = null;
  calLive = null;
  setTimeout(loadCalibration, 400);
}

// startSweep begins a measurement. The confirmation names the two things that
// are actually about to happen — the node leaves the air, and the operator has
// to transmit — because a sweep nobody keys up for measures nothing and looks
// like a broken feature.
async function startSweep() {
  if (calBusy) return;
  const c = calib || {};
  let msg = "Start the calibration sweep?\n\n";
  msg += "Set your radio to DMR on " + mhz(c.rx_freq_hz) + " MHz, colour code " + (c.color_code || 1) + ".\n";
  msg += "You will need to hold PTT when it asks — it measures what your radio sends.\n\n";
  if (c.host_running) msg += "This node goes off the air while it runs, and comes back afterwards.";
  if (!confirm(msg)) return;

  calBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/cal/sweep", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ stop_host: true }),
    });
    const body = await r.json().catch(() => ({}));
    if (r.status === 409) {
      banner(body.error || "Something else is using the modem.", "bad");
    } else if (!r.ok) {
      throw new Error(body.error || "the sweep could not be started");
    } else {
      calib = Object.assign({}, calib, { job: body.job });
      banner(msg("startSweep.sweepingKeyRadioWhen"), "ok");
      watchCal();
    }
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  calBusy = false;
  renderPanel();
}

async function cancelSweep() {
  try {
    await fetch("/api/cal/cancel", { method: "POST" });
    banner(msg("cancelSweep.stoppingSweepNodeComes"), "ok");
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  setTimeout(loadCalibration, 600);
}

// applyOffset writes the measured offset. It is a separate button from the
// sweep on purpose (RFC-0021 §7): measuring and changing the node are different
// acts, and the curve is on screen for the operator to disagree with first.
async function applyOffset() {
  if (calBusy) return;
  const res = (calib && ((calib.job && calib.job.result) || (calib.last && calib.last.result))) || null;
  if (!res || !res.best) return;
  if (!confirm("Write " + offsetText(String(res.best.offset_hz)) + " to this node's RX and TX offsets?\n\nApply the configuration afterwards for MMDVM-Host to pick it up.")) return;

  calBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/cal/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || "the offset could not be applied");
    banner((body.changed || []).join(", ") || "Offset applied.", "ok");
    // Re-read the store: the offsets on the General tab have just changed under
    // the editor, and an operator who switches tabs should not find the old
    // numbers still in the fields.
    await load();
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  calBusy = false;
  await loadCalibration();
}

// flashFirmware starts a flash. The confirmation is explicit about the two
// things an operator is actually risking — time off the air, and a modem that
// stops working if the image is wrong — and about the fact that a GPIO board
// survives an interrupted write.
async function flashFirmware() {
  if (fwBusy) return;
  const fw = firmware || {};
  const sel = document.getElementById("fw-variant");
  const variant = sel ? sel.value : "";
  const what = variant || (fw.match && fw.match.id) || "the matching firmware";

  let msg = "Write " + what + " to the modem?\n\n";
  if (fw.host_running) msg += "This node goes off the air for about a minute while it happens.\n\n";
  msg += "If it is interrupted the board stays reachable and you can flash again.";
  if (!confirm(msg)) return;

  fwBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/flash", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ variant_id: variant, stop_host: true }),
    });
    const body = await r.json().catch(() => ({}));
    if (r.status === 409) {
      banner(body.error || "Something else is using the modem.", "bad");
    } else if (!r.ok) {
      throw new Error(body.error || "the flash could not be started");
    } else {
      firmware = Object.assign({}, firmware, { job: body.job });
      banner(msg("flashFirmware.flashingDoNotPower"), "ok");
      watchFlash();
    }
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  fwBusy = false;
  renderPanel();
}

// refreshCatalog fetches the signed catalog now rather than using the cached
// copy, for an operator who has just been told a new firmware exists.
async function refreshCatalog() {
  if (fwBusy) return;
  fwBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/flash/catalog", { method: "POST" });
    const body = await r.json().catch(() => ({}));
    firmware = body;
    banner(body.available ? "Firmware catalog " + (body.catalog_version || "") + "." : (body.reason || "The firmware catalog could not be fetched."), body.available ? "ok" : "bad");
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  fwBusy = false;
  renderPanel();
}

// tcxoText says whether the oscillator was reported by the modem or inferred
// from the board table, because the two are not the same claim.
function tcxoText(id) {
  if (!id.tcxo_hz) return "not reported by this firmware";
  const label = (id.tcxo_hz / 1e6).toString().replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "") + " MHz";
  return id.tcxo_assumed ? label + " (inferred, not reported)" : label;
}

// modeSupportText lists what the firmware carries, and says plainly when that
// list is an assumption rather than an answer — protocol-1 firmware reports no
// capabilities at all, and a guess must not read as a fact.
function modeSupportText(m) {
  if (!m) return "unknown";
  const names = { dstar: "D-Star", dmr: "DMR", ysf: "System Fusion", p25: "P25", nxdn: "NXDN", m17: "M17", fm: "FM", pocsag: "POCSAG" };
  const on = Object.keys(names).filter((k) => m[k]).map((k) => names[k]);
  const list = on.length ? on.join(", ") : "none";
  return m.known ? list : list + " (assumed — this firmware does not report its capabilities)";
}

function outcomeText(o) {
  switch (o) {
    case "modem": return "answered — this is the modem";
    case "silent": return "opened, said nothing";
    case "busy": return "in use by something else";
    case "bootloader": return "a board in its bootloader";
    default: return "could not be opened";
  }
}

// loadHardware fetches the hardware surface and repaints the tab if it is showing.
async function loadHardware() {
  try {
    hardware = await fetch("/api/hardware").then((r) => r.json());
  } catch {
    hardware = null;
  }
  if (state.tab === "hardware" || state.tab === "general") renderPanel();
}

// detectModem probes for a modem. stopHost authorises taking the port away from
// a running MMDVM-Host, which is a real interruption of service — so the daemon
// refuses without it, and the operator is asked here rather than in a flag.
async function detectModem(stopHost) {
  if (hwBusy) return;
  hwBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/hardware/detect", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ stop_host: !!stopHost }),
    });
    if (r.status === 409) {
      const body = await r.json().catch(() => ({}));
      hwBusy = false;
      if (confirm(msg("detectModem.modemUseMmdvmHost"))) {
        return detectModem(true);
      }
      banner(body.error || "The modem is in use.", "bad");
      renderPanel();
      return;
    }
    if (!r.ok) throw new Error((await r.text()).trim());
    hardware = await r.json();
    banner(hardware.detected && hardware.detected.identity
      ? "Found " + (hardware.detected.identity.description || hardware.detected.identity.hw_type)
      : "No modem answered on any port.", hardware.detected && hardware.detected.identity ? "ok" : "bad");
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  hwBusy = false;
  renderPanel();
}

// adoptBoard writes the detection into the config. It reloads the whole config
// afterwards because the modem section it just changed is edited on the General
// tab too, and leaving those two views disagreeing is the bug this tab exists to
// prevent.
async function adoptBoard() {
  if (hwBusy) return;
  const sel = document.getElementById("hw-board");
  hwBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/hardware/adopt", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ board_id: sel ? sel.value : "" }),
    });
    if (!r.ok) throw new Error((await r.text()).trim());
    const body = await r.json();
    hardware = body.hardware;
    const changed = (body.adopted && body.adopted.changed) || [];
    banner(changed.length ? "Adopted — " + changed.join(", ") : "Already configured for this modem.", "ok");
    hwBusy = false;
    await load();          // the modem section changed underneath the General tab
    await loadHardware();
    return;
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  hwBusy = false;
  renderPanel();
}

// fixUART frees the GPIO serial port. It always ends in a reboot prompt, because
// config.txt and cmdline.txt are read at boot and the repair changes nothing
// until then — telling an operator it worked while the symptom persists would be
// worse than not offering it.
async function fixUART() {
  if (hwBusy) return;
  if (!confirm(msg("fixUART.freeGpioSerialPort"))) return;
  hwBusy = true;
  renderPanel();
  try {
    const r = await fetch("/api/hardware/uart", { method: "POST" });
    if (!r.ok) throw new Error((await r.text()).trim());
    const body = await r.json();
    const res = body.result || {};
    if (!res.applicable) {
      banner(msg("fixUART.hostHasNoRaspberry"), "bad");
    } else if (res.reboot_required) {
      banner("Done: " + (res.changed || []).join("; ") + ". Reboot the node for it to take effect.", "ok");
    } else {
      banner(msg("fixUART.gpioSerialPortWas"), "ok");
    }
  } catch (err) {
    banner(String(err.message || err), "bad");
  }
  hwBusy = false;
  await loadHardware();
  renderPanel();
}

// hasCheck reports whether a real check timestamp is present (the Go zero time
// serializes as a 0001 date, which is "never checked").
function hasCheck(st) { return hasStamp(st.last_check); }
function hasStamp(v) { return v && !String(v).startsWith("0001"); }
function fmtWhen(iso) { try { return new Date(iso).toLocaleString(); } catch (_) { return iso; } }

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
// --- inline help (#135) ---------------------------------------------------
// Help is attached after render rather than threaded through row()/input()/
// toggle(): the panels build controls a dozen different ways — bare selects,
// datalists, unit wrappers, bespoke rows like nodeLockRow — and every one of them
// already carries data-sec/data-key or data-toggle. Walking the rendered DOM
// therefore covers all of them uniformly, and any control added later gets help
// for free just by appearing in the HELP set.
//
// The body is .sr-only when collapsed rather than hidden: aria-describedby cannot
// reach display:none content, and a screen-reader user should get the description
// without depending on a sighted interaction having happened. The "?" button
// governs visual disclosure only, so help is never hover-only.
let openHelp = new Set();
function helpId(key) { return "wp-help-" + key.replace(/[^A-Za-z0-9_-]/g, "-"); }

function enhanceHelp(box) {
  box.querySelectorAll("[data-toggle], [data-sec][data-key]").forEach((ctrl) => {
    const key = ctrl.dataset.toggle || ctrl.dataset.sec + "." + ctrl.dataset.key;
    if (!HELP.has(key)) return;
    const text = msg("help." + key);
    const rowEl = ctrl.closest(".row, .toggle-row");
    // One help block per row: a row with several controls (an IPv4 editor, say)
    // would otherwise get one per field.
    if (!rowEl || rowEl.querySelector(".row-help")) return;
    const host = rowEl.querySelector("label, .name");
    if (!host) return;

    const id = helpId(key);
    const open = openHelp.has(id);
    const label = (host.textContent || "").trim();

    const btn = el("button", "help-btn");
    btn.type = "button";
    btn.dataset.help = id;
    btn.setAttribute("aria-expanded", String(open));
    btn.setAttribute("aria-controls", id);
    btn.innerHTML = `<span aria-hidden="true">?</span><span class="sr-only">${esc(msg("help.whatIs", { label }))}</span>`;
    host.appendChild(btn);

    const body = el("p", "row-help" + (open ? "" : " sr-only"), text);
    body.id = id;
    rowEl.appendChild(body);
    if (rowEl.classList.contains("toggle-row")) rowEl.classList.add("has-help");
    ctrl.setAttribute("aria-describedby", id);
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
    case "hardware":     box.innerHTML = panelHardware(); break;
    case "setup":        box.innerHTML = panelDisplay(); break;
    case "lcd":          box.innerHTML = panelLCD(); break;
    case "modes":        box.innerHTML = panelModesSection(); break;
    case "profiles":     box.innerHTML = panelProfiles(); break;
    case "station":      box.innerHTML = panelStation(); break;
    case "updates":      box.innerHTML = panelUpdates(); break;
    case "brandmeister": box.innerHTML = panelBrandmeister(); break;
    case "expert":       box.innerHTML = panelExpert(c, state.health); break;
    case "gateways":     box.innerHTML = panelGateways(); break;
    case "network":      box.innerHTML = panelNetwork(); break;
    default:             box.innerHTML = "";
  }
  enhanceA11y();
  // After enhanceA11y, so a help button appended to a <label> cannot be picked up
  // as that label's control when it assigns for/id pairs.
  enhanceHelp(box);
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
// A tab's nav group is the crumb prefix (D1) — "SYSTEM / GENERAL" files General
// under SYSTEM. Filing a new tab is therefore just a matter of its crumb.
function groupOf(t) { return String(WPI18n.base("tab." + t.id + ".crumb")).split("/")[0].trim().toUpperCase() || "OTHER"; }

// The sidebar heading for a group. A crumb prefix with no catalog entry falls
// back to the prefix itself, so an unrecognised group still renders its tabs
// rather than a bare key.
function groupLabel(name) {
  const key = "nav.group." + name.toLowerCase().replace(/[^a-z0-9]+/g, "");
  const s = msg(key);
  return s === key ? name : s;
}

// Groups in NAV_GROUPS order, then any prefix not on that list in first-seen order,
// so an unrecognised crumb can never silently drop its tab off the sidebar.
function navGroups() {
  const seen = [];
  TABS.forEach((t) => { const g = groupOf(t); if (!seen.includes(g)) seen.push(g); });
  const order = NAV_GROUPS.filter((g) => seen.includes(g)).concat(seen.filter((g) => !NAV_GROUPS.includes(g)));
  return order.map((name) => ({ name, id: "navg-" + name.toLowerCase().replace(/[^a-z0-9]+/g, "-"), items: TABS.filter((t) => groupOf(t) === name) }));
}

// Group expansion persists across reloads (D2). `navOpen` holds only the groups an
// operator has explicitly opened or closed; anything absent falls back to the
// default, which is collapsed except for the group holding the active tab. That
// keeps the sidebar short on arrival — the point of grouping — while the section
// being edited is never hidden behind a disclosure.
function loadNavOpen() {
  try {
    const raw = JSON.parse(localStorage.getItem(NAV_OPEN_KEY) || "{}");
    return raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
  } catch (e) { return {}; }
}
function saveNavOpen() {
  try { localStorage.setItem(NAV_OPEN_KEY, JSON.stringify(navOpen)); } catch (e) { /* storage blocked — expansion is per-session */ }
}
function activeGroup() {
  const t = TABS.find((x) => x.id === state.tab);
  return t ? groupOf(t) : groupOf(TABS[0]);
}
function groupExpanded(name) {
  if (Object.prototype.hasOwnProperty.call(navOpen, name)) return !!navOpen[name];
  return name === activeGroup();
}
function toggleGroup(name) {
  navOpen[name] = !groupExpanded(name);
  saveNavOpen();
  renderNav();
  const again = document.getElementById(navGroups().find((g) => g.name === name).id);
  const btn = again && document.querySelector(`[aria-controls="${CSS.escape(again.id)}"]`);
  if (btn) btn.focus(); // keep a keyboard user on the header they just toggled
}

function renderNav() {
  const nav = document.getElementById("nav");
  const body = el("div", "nav-body");
  if (NAV_NARROW.matches) buildNavTiles(body);
  else buildNavGroups(body);
  const old = nav.querySelector(".nav-body");
  if (old) old.replaceWith(body);
  else nav.appendChild(body);
}

// Desktop (D2): each group is a disclosure — a real <button> in a heading, so it is
// tab-reachable and Enter/Space toggle it natively, controlling the item list by id.
function buildNavGroups(body) {
  navGroups().forEach((g) => {
    const open = groupExpanded(g.name);
    const wrap = el("div", "nav-group");
    const head = el("h2", "nav-group-h");
    const btn = el("button", "nav-group-btn" + (open ? " open" : ""));
    btn.type = "button";
    btn.setAttribute("aria-expanded", String(open));
    btn.setAttribute("aria-controls", g.id);
    btn.innerHTML = `<span class="chev" aria-hidden="true"></span><span class="gname">${esc(g.name)}</span><span class="gcount" aria-hidden="true">${g.items.length}</span>`;
    btn.onclick = () => toggleGroup(g.name);
    head.appendChild(btn);
    wrap.appendChild(head);

    const items = el("div", "nav-group-items");
    items.id = g.id;
    if (!open) items.hidden = true;
    g.items.forEach((t) => items.appendChild(navItem(t)));
    wrap.appendChild(items);
    body.appendChild(wrap);
  });
}

function navItem(t) {
  const on = t.id === state.tab;
  const item = el("button", "nav-item" + (on ? " on" : ""));
  item.type = "button";
  if (on) item.setAttribute("aria-current", "page");
  item.setAttribute("aria-label", msg("nav.itemLabel", { label: tabLabel(t), sub: tabSub(t) }));
  item.innerHTML = `<div class="bar" aria-hidden="true"></div><div class="tag" aria-hidden="true">${esc(t.tag)}</div><div><div class="label">${esc(tabLabel(t))}</div><div class="sub">${esc(tabSub(t))}</div></div>`;
  item.onclick = () => selectTab(t.id);
  return item;
}

// Narrow viewports (D3): a grid of large touch tiles sectioned by group, replacing
// the old horizontal scroll strip. Picking a tile swaps the grid for the panel and a
// back control, so a phone shows one thing at a time and the page never scrolls
// sideways.
function buildNavTiles(body) {
  const back = el("button", "nav-back");
  back.type = "button";
  back.innerHTML = `<span class="chev" aria-hidden="true">←</span><span>${esc(msg("nav.allSettings"))}</span>`;
  back.onclick = () => showNavGrid(true);
  body.appendChild(back);

  const grid = el("div", "nav-grid");
  navGroups().forEach((g) => {
    const head = el("h2", "tile-sec");
    head.id = "tile-" + g.id;
    head.textContent = groupLabel(g.name);
    grid.appendChild(head);
    const row = el("div", "tile-row");
    row.setAttribute("role", "group");
    row.setAttribute("aria-labelledby", head.id);
    g.items.forEach((t) => {
      const on = t.id === state.tab;
      const tile = el("button", "nav-tile" + (on ? " on" : ""));
      tile.type = "button";
      if (on) tile.setAttribute("aria-current", "page");
      tile.innerHTML = `<span class="tag" aria-hidden="true">${esc(t.tag)}</span><span class="label">${esc(tabLabel(t))}</span><span class="sub">${esc(tabSub(t))}</span>`;
      tile.onclick = () => selectTab(t.id);
      row.appendChild(tile);
    });
    grid.appendChild(row);
  });
  body.appendChild(grid);
}

function setNavView(v) {
  navView = v;
  document.documentElement.setAttribute("data-nav-view", v);
}

// Back to the tile grid. The page head stops describing a section that is no longer
// on screen, but APPLY/RESET stay exactly where they were so unsaved edits are never
// stranded behind the back button (D6).
function showNavGrid(focus) {
  setNavView("grid");
  document.getElementById("crumb").textContent = msg("settings.crumb");
  document.getElementById("title").textContent = msg("settings.title");
  document.getElementById("desc").textContent = msg("settings.chooseSection");
  renderNav();
  if (!focus) return;
  const first = document.querySelector("#nav .nav-tile.on") || document.querySelector("#nav .nav-tile");
  if (first) first.focus();
}

// Re-render on a breakpoint crossing so the nav carries the semantics of the layout
// actually on screen — no stale aria-expanded on a tile grid, no orphaned grid view
// once the sidebar is back.
NAV_NARROW.addEventListener("change", () => {
  if (!NAV_NARROW.matches && navView === "grid") { selectTab(state.tab, state.sub); return; }
  renderNav();
});

// --- deep links (D5) ------------------------------------------------------
// The retired per-mode ids still resolve: "#dmr" lands on the Modes tab with the DMR
// sub-tab active. The canonical form is "#modes/<sub>"; the last sub-tab is
// remembered so a bare "#modes" reopens where the operator left off.
function isModeSub(id) { return MODE_SUBS.some((m) => m.id === id); }
function storedModeSub() {
  try { const v = localStorage.getItem(MODE_SUB_KEY); if (isModeSub(v)) return v; } catch (e) { /* storage blocked */ }
  return MODE_SUBS[0].id;
}
function currentModeSub() { return isModeSub(state.sub) ? state.sub : MODE_SUBS[0].id; }
// True when the given mode's panel is the one on screen — the lazy reflector/master
// loads use this to decide whether a fetch should repaint the current panel.
function showingMode(k) { return state.tab === "modes" && currentModeSub() === k; }

function resolveTarget(raw) {
  const parts = String(raw || "").split("/");
  let id = safeDecode(parts[0]).trim();
  let sub = safeDecode(parts[1]).trim();
  if (isModeSub(id)) { sub = id; id = "modes"; }   // legacy per-mode deep link
  if (id === "mode") id = "modes";
  if (!TABS.some((x) => x.id === id)) id = TABS[0].id;
  if (id === "modes") sub = isModeSub(sub) ? sub : storedModeSub();
  return { id, sub };
}
function safeDecode(s) { try { return decodeURIComponent(s || ""); } catch (e) { return String(s || ""); } }

function crumbFor(t) {
  if (t.id !== "modes") return tabCrumb(t);
  // The mode's own name is a protocol token and stays as it is; only the frame
  // around it is translated.
  const m = MODE_SUBS.find((x) => x.id === currentModeSub()) || MODE_SUBS[0];
  return msg("tab.modes.crumbForMode", { mode: m.crumb });
}

function selectTab(id, sub) {
  const target = resolveTarget(sub ? id + "/" + sub : id);
  state.tab = target.id;
  if (target.id === "modes") {
    state.sub = target.sub;
    try { localStorage.setItem(MODE_SUB_KEY, target.sub); } catch (e) { /* storage blocked — sub-tab is per-session */ }
  }
  const t = TABS.find((x) => x.id === state.tab);
  const hash = "#" + (state.tab === "modes" ? "modes/" + currentModeSub() : state.tab);
  if (location.hash !== hash) history.replaceState(null, "", hash);
  // A deep link can land on a tab inside a group the operator explicitly collapsed.
  // Drop that override rather than force a `true`: the group then falls back to the
  // default, which opens it precisely because it now holds the active tab (D2), and
  // collapsing it again still sticks.
  const g = groupOf(t);
  if (!groupExpanded(g)) { delete navOpen[g]; saveNavOpen(); }
  setNavView("panel");
  document.getElementById("crumb").textContent = crumbFor(t);
  document.getElementById("title").textContent = tabTitle(t);
  document.getElementById("desc").textContent = tabDesc(t);
  renderNav();
  renderPanel();
  // The Network tab shows live system state, fetched on demand (not part of the
  // store config load).
  if (state.tab === "network") loadNetwork();
  // The Expert tab's override view is fetched on demand (read-only, RFC-0005),
  // re-fetched each open so it reflects the current store render.
  if (state.tab === "expert") loadOverrides();
  // Connection profiles are fetched on demand (RFC-0006), refreshed each open so
  // the ACTIVE badge reflects the live store.
  if (state.tab === "profiles") loadProfiles();
  // The Updates tab reads installed/available versions from the daemon on demand
  // (RFC-0014), re-fetched each open so it reflects the live apt/dpkg state.
  if (state.tab === "updates") loadUpdateStatus();
}

// A hash set from outside the page — a link to #modes/dmr elsewhere in the UI, a
// pasted deep link on an already-open tab, an edited address bar — has to move the
// selection too. selectTab's own replaceState does not fire this event, so there is
// no loop; the guard just avoids a pointless re-render when the hash is already the
// one on screen.
window.addEventListener("hashchange", () => {
  const raw = (location.hash || "").slice(1);
  const t = resolveTarget(raw);
  if (t.id === state.tab && (t.id !== "modes" || t.sub === currentModeSub())) return;
  selectTab(t.id, t.sub);
});

// Switch the Modes sub-tab. Only the rendered panel changes: `edit` and the per-
// section `dirty` set are untouched, so unsaved edits survive the switch (D6).
function selectModeSub(sub) {
  if (!isModeSub(sub) || sub === currentModeSub()) return;
  selectTab("modes", sub);
  const btn = document.querySelector(`[data-modesub="${CSS.escape(sub)}"]`);
  if (btn) btn.focus(); // the tablist re-renders; keep the keyboard user on their tab
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
  toggle.title = mode === "light" ? msg("theme.switchToDark") : msg("theme.switchToLight");
  toggle.setAttribute("aria-label", msg("theme.toggleLight"));
  toggle.setAttribute("aria-pressed", String(mode === "light"));
  toggle.textContent = mode === "light" ? msg("theme.light") : msg("theme.dark");
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
    const themeName = msg("theme." + th.key);
    s.title = themeName;
    s.setAttribute("aria-label", msg("theme.swatchLabel", { theme: themeName }));
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
  // Hostlist supply state (#138): fetched with the lists so a panel can explain an
  // empty picker instead of just showing one.
  try {
    hostLists = await fetch("/api/hostlists").then((r) => r.json()) || [];
    renderPanel();
  } catch { /* older daemon without the endpoint — panels just stay quiet */ }
  try {
    ysfRefs = await fetch("/api/ysf/reflectors").then((r) => r.json());
    if (showingMode("ysf")) renderPanel();
  } catch { /* offline — the picker still accepts a typed id */ }
  try {
    p25Refs = await fetch("/api/p25/reflectors").then((r) => r.json());
    if (showingMode("p25")) renderPanel();
  } catch { /* offline — the picker still accepts a typed TG */ }
  try {
    nxdnRefs = await fetch("/api/nxdn/reflectors").then((r) => r.json());
    if (showingMode("nxdn")) renderPanel();
  } catch { /* offline — the picker still accepts a typed TG */ }
  try {
    dstarRefs = await fetch("/api/dstar/reflectors").then((r) => r.json());
    if (showingMode("dstar")) renderPanel();
  } catch { /* offline — the picker still accepts a typed reflector */ }
  try {
    m17Refs = await fetch("/api/m17/reflectors").then((r) => r.json());
    if (showingMode("m17")) renderPanel();
  } catch { /* offline — the picker still accepts a typed reflector */ }
  try {
    dmrMasters = await fetch("/api/dmr/masters").then((r) => r.json()) || [];
    if (state.tab === "brandmeister") renderPanel();
  } catch { /* offline — the master dropdowns show what's cached (may be empty) */ }
  try {
    dmrTGs = await fetch("/api/dmr/talkgroups").then((r) => r.json()) || [];
    if (showingMode("dmr")) renderPanel();
  } catch { /* offline — the TG picker still accepts a typed number */ }
  // Modem hardware is live system state like the network status, not store
  // config: the last detection, the board table, and the GPIO UART's
  // availability. The General tab reads it too, for the board picker.
  loadHardware();
  loadFirmware();
  loadCalibration();
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

// loadUpdateStatus fetches the stack update status (installed versions, cached
// available updates, history) and repaints the Updates tab (RFC-0014).
async function loadUpdateStatus() {
  try {
    stackStatus = await fetch("/api/update/stack").then((r) => r.json());
  } catch {
    stackStatus = null;
  }
  if (state.tab === "updates") renderPanel();
}

// stackCheckNow runs an on-demand check of both update paths — the signed
// waypointd manifest, and the apt check against the Waypoint source where the repo
// is configured — then repaints with the fresh availability. This is the operator
// asking, so it runs whatever the automatic-check preference says (#15).
async function stackCheckNow() {
  if (stackBusy) return;
  stackBusy = true;
  const btn = document.getElementById("stack-check");
  if (btn) { btn.textContent = "CHECKING…"; btn.disabled = true; }
  const failures = [];
  const attempt = async (url, opts, okStatus) => {
    try {
      const r = await fetch(url, opts);
      if (!r.ok && r.status !== okStatus) failures.push((await r.text()).trim());
    } catch (err) {
      failures.push(String(err.message || err));
    }
  };
  // 501 from the manifest check just means this node has no update URL configured.
  await attempt("/api/update/check", undefined, 501);
  if ((stackStatus || {}).configured) await attempt("/api/update/stack/check", { method: "POST" });
  if (failures.length) banner(failures.join(" · "), "bad");
  stackBusy = false;
  await loadUpdateStatus();
}

// stackApplyNow starts a health-gated apply (the daemon runs it in the background)
// and polls the status until it settles to confirmed/reverted.
async function stackApplyNow() {
  if (stackBusy) return;
  if (!confirm(msg("stackApplyNow.applyAvailableStackUpdates"))) return;
  stackBusy = true;
  const btn = document.getElementById("stack-apply");
  if (btn) { btn.textContent = "STARTING…"; btn.disabled = true; }
  try {
    const r = await fetch("/api/update/stack/apply", { method: "POST" });
    if (!r.ok && r.status !== 202) throw new Error((await r.text()).trim());
    banner(msg("stackApplyNow.updateStartedHealthChecking"), "ok");
  } catch (err) {
    banner(String(err.message || err), "bad");
  } finally {
    stackBusy = false;
    startStackPolling();
  }
}

// startStackPolling refreshes the status every few seconds while an apply runs,
// stopping once the daemon reports it is no longer applying.
function startStackPolling() {
  if (stackPoll) return;
  stackPoll = setInterval(async () => {
    await loadUpdateStatus();
    if (!stackStatus || !stackStatus.applying) {
      clearInterval(stackPoll);
      stackPoll = null;
    }
  }, 3000);
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
      if (confirm(msg("importProfile.profileNameAlreadyExists"))) {
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
  if (!confirm(msg("applyImport.importScannedConfigOverwrites"))) return;
  importBusy = true; renderPanel();
  try {
    const r = await fetch("/api/import/apply", importFetchInit(importInput));
    if (!r.ok) alert("Import failed: " + (await r.text()));
    else {
      alert(msg("applyImport.importedReviewSettingsThen"));
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
    banner(msg("applyNetwork.networkChangeAppliedConfirm"), "ok");
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
  // --- inline help disclosure (#135) ---
  // Toggled in place rather than via renderPanel: the text is already in the DOM,
  // so this only flips its visibility, and skipping the re-render keeps the
  // operator's scroll position and focus exactly where they were. preventDefault
  // stops a <label> that wraps the button forwarding the click to its control.
  const hb = e.target.closest("[data-help]");
  if (hb) {
    e.preventDefault();
    const id = hb.dataset.help;
    const open = !openHelp.has(id);
    if (open) openHelp.add(id); else openHelp.delete(id);
    const body = document.getElementById(id);
    if (body) body.classList.toggle("sr-only", !open);
    hb.setAttribute("aria-expanded", String(open));
    return;
  }
  // --- Modes sub-tab strip (D4) ---
  const ms = e.target.closest("[data-modesub]");
  if (ms) { selectModeSub(ms.dataset.modesub); return; }
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
  // --- modem hardware (#18) ---
  if (e.target.id === "hw-detect") { detectModem(false); return; }
  if (e.target.id === "hw-adopt") { adoptBoard(); return; }
  if (e.target.id === "hw-uart") { fixUART(); return; }
  // --- modem firmware (#19) ---
  if (e.target.id === "fw-flash") { flashFirmware(); return; }
  if (e.target.id === "fw-refresh") { refreshCatalog(); return; }
  // --- modem calibration (#20) ---
  if (e.target.id === "cal-sweep") { startSweep(); return; }
  if (e.target.id === "cal-cancel") { cancelSweep(); return; }
  if (e.target.id === "cal-apply") { applyOffset(); return; }
  // --- software updates (RFC-0014) ---
  if (e.target.id === "stack-check") { stackCheckNow(); return; }
  if (e.target.id === "stack-apply") { stackApplyNow(); return; }
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
  // Modes sub-tab strip: arrows move between tabs, Home/End jump to the ends
  // (WAI-ARIA tabs pattern). Enter/Space need no handler — they are real buttons.
  const mt = t.closest && t.closest("[data-modesub]");
  if (mt) {
    const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    let next = null;
    if (step) {
      const i = MODE_SUBS.findIndex((m) => m.id === mt.dataset.modesub);
      next = MODE_SUBS[(i + step + MODE_SUBS.length) % MODE_SUBS.length].id;
    } else if (e.key === "Home") next = MODE_SUBS[0].id;
    else if (e.key === "End") next = MODE_SUBS[MODE_SUBS.length - 1].id;
    if (next) { e.preventDefault(); selectModeSub(next); }
    return;
  }
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

// Every panel is built from template literals that re-read msg() on each render,
// so a language change is a re-render of the chrome plus the open panel.
function mountLanguagePicker() { WPI18n.renderPicker(document.getElementById("lang-pick")); }
addEventListener("wp-lang-changed", () => {
  mountLanguagePicker();
  renderThemes();
  renderNav();
  const tab = TABS.find((x) => x.id === state.tab);
  if (tab) {
    document.getElementById("crumb").textContent = crumbFor(tab);
    document.getElementById("title").textContent = tabTitle(tab);
    document.getElementById("desc").textContent = tabDesc(tab);
  }
  renderPanel();
});

// Nothing below paints until the catalogs are in: msg() would answer with bare
// keys, and i18n.js re-applying the static markup afterwards would overwrite what
// had already rendered. WPI18n.ready never rejects — a missing catalog degrades
// to English inside i18n.js.
WPI18n.ready.then(() => {
  renderNav();
  renderThemes();
  mountLanguagePicker();
  {
    // A deep link opens straight onto its panel — including the retired per-mode ids.
    // Without one, a narrow viewport opens on the tile grid (there is nothing to go
    // "back" to yet); the sidebar layout has no grid view and just lands on the first
    // tab, as before.
    const target = (location.hash || "").slice(1);
    selectTab(target || "general");
    if (!target && NAV_NARROW.matches) showNavGrid(false);
  }
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
});
