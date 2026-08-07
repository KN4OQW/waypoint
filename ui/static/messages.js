/* Waypoint messages page.
 *
 * A thin wrapper over msgstate.js, which holds every decision worth testing. This
 * file is DOM and fetch: it reads the form, calls the API, and renders rows.
 *
 * REST is authoritative; the event stream is a poke. A message event carries the
 * id and the state and deliberately NOT the text, so arriving at one means
 * "re-read the list", never "render this". That is the same contract the rest of
 * the dashboard uses and the reason the event log can be republished to MQTT
 * without republishing anyone's correspondence.
 */
"use strict";

const $ = (s) => document.querySelector(s);
const t = (key, params) => WPI18n.t(key, params); // message catalogs — see i18n.js

let messages = [];

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => (
    { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
  ));
}

function fmtTime(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "—" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

// --- compose ----------------------------------------------------------------

// updateBudget runs on every keystroke. It is also what enables and disables the
// send button, so the form can never submit something the API would reject for a
// reason the operator could have been shown first.
function updateBudget() {
  const text = $("#msg-text").value;
  const b = WPMsg.budget(text);
  const el = $("#msg-budget");
  el.textContent = b.over
    ? t("messages.budget.over", { n: -b.remaining })
    : t("messages.budget.left", { n: b.remaining });
  el.className = "budget" + (b.over ? " over" : b.near ? " near" : "");

  const can = WPMsg.sendable($("#msg-id").value, text);
  $("#msg-send").disabled = !can.ok;
  // An empty form is not an error state — say nothing until there is something
  // to say, or the page greets the operator with a complaint.
  const note = $("#send-note");
  if (can.ok || (!$("#msg-id").value && !text)) {
    if (note.dataset.sticky !== "1") note.textContent = "";
    return;
  }
  note.dataset.sticky = "";
  note.className = "sendnote bad";
  note.textContent = t(can.reason, "");
}

function say(msg, bad) {
  const note = $("#send-note");
  note.className = "sendnote" + (bad ? " bad" : "");
  note.textContent = msg;
  note.dataset.sticky = "1";
}

async function send(ev) {
  ev.preventDefault();
  const id = $("#msg-id").value.trim();
  const text = $("#msg-text").value;
  const can = WPMsg.sendable(id, text);
  if (!can.ok) {
    say(t(can.reason), true);
    return;
  }
  $("#msg-send").disabled = true;
  try {
    const r = await fetch("/api/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ dmr_id: Number(id), text: text }),
    });
    const body = await r.json().catch(() => ({}));
    if (r.status === 202) {
      // Queued, not sent. Saying "sent" here would be the first lie in a chain
      // that ends with an operator wondering why a radio never answered.
      say(t("messages.sent.queued"), false);
      $("#msg-text").value = "";
      messages = WPMsg.merge(messages, [body]);
      render();
    } else {
      say(body.error || t("messages.err.failed"), true);
    }
  } catch (e) {
    say(t("messages.err.offline"), true);
  }
  updateBudget();
  load();
}

// --- list -------------------------------------------------------------------

function render() {
  const tb = $("#msglog tbody");
  tb.innerHTML = "";
  for (const m of messages) {
    const out = WPMsg.outbound(m);
    const tr = document.createElement("tr");
    const reason = m.state === WPMsg.FAILED && m.reason
      ? `<span class="msg-reason">${esc(m.reason)}</span>` : "";
    tr.innerHTML =
      `<td>${esc(fmtTime(m.created_at))}</td>` +
      `<td><span class="msg-dir">${out ? "TO" : "FROM"}</span> ${esc(m.peer)}</td>` +
      `<td class="msg-body">${esc(m.text)}</td>` +
      `<td class="msg-state ${WPMsg.stateClass(m)}">` +
      `<span aria-hidden="true">${WPMsg.stateMark(m)}</span> ` +
      `${esc(t(WPMsg.stateKey(m)))}${reason}</td>`;
    tb.appendChild(tr);
  }
  $("#msglog-empty").style.display = messages.length ? "none" : "";
}

// load re-reads the list, and doubles as this page's reachability check.
//
// The connection LED cannot come from EventSource.onopen: a stream that has sent
// no events yet has not "opened" as far as the browser is concerned, so a quiet
// node would sit on "connecting…" forever. The dashboard drives its LED from
// polling for exactly this reason; so does this.
async function load() {
  try {
    const r = await fetch("/api/messages?limit=200");
    if (!r.ok) return;
    const body = await r.json();
    messages = WPMsg.merge(messages, body.messages || []);
    render();
    WPChrome.setConn(true);
  } catch (e) {
    WPChrome.setConn(false);
  }
}

// --- node state -------------------------------------------------------------

// The relay is what makes any of this work, and it is off by default. A page that
// silently did nothing would send an operator to the logs; this says so up front.
//
// The relay's state comes from /api/status, not from the configuration. Two
// reasons, and the second was measured rather than reasoned: the status plane
// reports whether the relay is actually RUNNING, which is the question, and the
// configuration API does not expose the dmrnet section at all — reading a flag
// from it reported "off" on a node that was transmitting perfectly well.
async function loadNode() {
  try {
    const c = await (await fetch("/api/config")).json();
    // [DMR] Id first, then the station id, whose view field is dmr_id and not id.
    const id = (c.dmr && c.dmr.id) || (c.general && c.general.dmr_id) || "";
    $("#st-id").textContent = id || "—";
  } catch (e) { /* unauthenticated or offline; the gate will have redirected */ }
  await loadRelay();
}

// RELAY_LINK is the name waypointd publishes the relay under (cmd/waypointd
// dmrshim.go). A node with the relay switched off publishes no such link at all,
// which is the "off" case rather than a missing one.
const RELAY_LINK = "DMR Message Relay";

async function loadRelay() {
  try {
    const s = await (await fetch("/api/status")).json();
    const link = (s.networks || {})[RELAY_LINK];
    const state = link ? WPLink.linkState(link) : "down";
    const on = state === "up" || state === "unknown";
    $("#st-relay").textContent = on ? t("messages.relay.on") : t("messages.relay.off");
    $("#st-relay").className = "v" + (on ? " accent" : "");
    if (!on && $("#send-note").dataset.sticky !== "1") say(t("messages.relay.hint"), true);
  } catch (e) { /* the LED already reports an unreachable node */ }
}

function connect() {
  const es = new EventSource("/api/events");
  es.onopen = () => WPChrome.setConn(true);
  es.onerror = () => WPChrome.setConn(false); // EventSource reconnects on its own
  es.onmessage = (ev) => {
    let e;
    try { e = JSON.parse(ev.data); } catch (err) { return; }
    // A poke, not the payload: the event has no text in it by design.
    if (WPMsg.isMessageEvent(e)) load();
  };
}

function boot() {
  $("#compose").addEventListener("submit", send);
  $("#msg-text").addEventListener("input", updateBudget);
  $("#msg-id").addEventListener("input", updateBudget);
  updateBudget();
  loadNode();
  load();
  connect();
  // A slow poll as well as the event poke: it keeps the connection LED honest
  // when nothing is happening, and recovers a list that a dropped event missed.
  setInterval(load, 15000);
  setInterval(loadRelay, 15000);
}

// The shell chrome first, then the page. Both wait for the catalogs for the same
// reason app.js does: t() before the fetch lands answers with bare keys, and
// i18n.js re-applying the static markup afterwards would stamp them back over
// anything already rendered. WPI18n.ready never rejects.
WPChrome.applyMode(WPChrome.currentMode());
WPChrome.applySavedTheme();
WPI18n.ready.then(() => {
  WPChrome.renderThemes();
  WPChrome.mountLanguagePicker();
  WPChrome.loadCallsign();
  boot();
});
