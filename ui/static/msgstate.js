/* Waypoint text messages: the logic the messages page needs, with no DOM in it.

   The two things worth getting right here are both about telling the truth.

   The LENGTH BUDGET is counted in UTF-16 code units, because that is what the
   on-air format carries: a character outside the BMP costs two. Counting
   characters would let an operator type 123 emoji, watch the counter say they
   were fine, and get a 413 back. JavaScript's own string.length is already in
   code units, which is the one place its historical oddity is exactly right.

   The SEND STATE mirrors the store's, including that `sent` does not mean
   delivered. Unconfirmed DMR data carries no acknowledgement, so there is no
   later fact to show and nothing here may imply one.

   Pure functions, no DOM, no globals, so ui/tests/msgstate.test.js drives them
   directly. Plain script in the same no-build style as linkstate.js/tzpicker.js:
   it attaches a WPMsg global for the browser and also exports for CommonJS so the
   Node test runner can require it. */
"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api; // node --test
  if (root) root.WPMsg = api;                                               // browser
})(typeof window !== "undefined" ? window : null, function () {
  // MAX_UNITS is dmrdata.MaxTextUnits. It comes from the TMS length field, which
  // the proven on-air construction writes as a single octet holding 2*units + 8 —
  // so 123 is the last value that cannot overflow it.
  const MAX_UNITS = 123;

  // Message states, matching internal/events. `sent` is terminal because there is
  // nothing after it that this node can know.
  const QUEUED = "queued";
  const TRANSMITTING = "transmitting";
  const SENT = "sent";
  const RECEIVED = "received";
  const FAILED = "failed";

  // units counts a string the way the radio does: UTF-16 code units, so an emoji
  // or any other astral character costs two. A null or undefined body is zero
  // rather than an exception — an empty form is a normal state, not an error.
  function units(text) {
    return typeof text === "string" ? text.length : 0;
  }

  // budget describes the length allowance for a body of text: how much is used,
  // how much is left, whether it fits, and whether it is close enough to warrant
  // saying so before the operator runs out mid-word.
  function budget(text) {
    const used = units(text);
    const remaining = MAX_UNITS - used;
    return {
      used: used,
      max: MAX_UNITS,
      remaining: remaining,
      over: remaining < 0,
      // "Near" starts with a fifth of the allowance left. Warning earlier is
      // noise; warning later is a warning that arrives after the decision.
      near: remaining >= 0 && remaining <= Math.floor(MAX_UNITS / 5),
    };
  }

  // sendable reports whether a form may be submitted, and why not when it may
  // not. The reason is a translation KEY rather than prose: this module has no
  // opinion about language.
  function sendable(dmrID, text) {
    if (!validID(dmrID)) return { ok: false, reason: "messages.err.id" };
    if (units(text) === 0) return { ok: false, reason: "messages.err.empty" };
    if (budget(text).over) return { ok: false, reason: "messages.err.long" };
    return { ok: true, reason: "" };
  }

  // validID accepts a 24-bit DMR ID, which is what the protocol carries. Zero is
  // not one: it addresses nobody.
  //
  // A string is accepted only if it is entirely digits. parseInt would take
  // "3180202abc" and quietly return 3180202, which is how a typo becomes a
  // message to a stranger.
  function validID(v) {
    let n = v;
    if (typeof v === "string") {
      if (!/^\d+$/.test(v.trim())) return false;
      n = Number(v.trim());
    }
    return Number.isInteger(n) && n > 0 && n <= 0xFFFFFF;
  }

  // stateClass drives the row's colour. There are three outcomes worth
  // distinguishing and they are not up/down: in flight, done, and failed.
  function stateClass(m) {
    switch (state(m)) {
      case FAILED: return "failed";
      case SENT:
      case RECEIVED: return "done";
      default: return "pending";
    }
  }

  // stateMark is the glyph beside the row. Colour is never the only carrier of
  // meaning in this dashboard, so each outcome gets its own mark.
  function stateMark(m) {
    switch (state(m)) {
      case FAILED: return "✗";
      case SENT: return "✓";
      case RECEIVED: return "▾";
      case TRANSMITTING: return "◌";
      default: return "…";
    }
  }

  // stateKey is the translation key for a state's label.
  function stateKey(m) {
    const s = state(m);
    switch (s) {
      case QUEUED:
      case TRANSMITTING:
      case SENT:
      case RECEIVED:
      case FAILED: return "messages.state." + s;
      default: return "messages.state.unknown";
    }
  }

  function state(m) {
    return m && typeof m.state === "string" ? m.state : "";
  }

  // outbound reports which way a message went. Anything that is not explicitly
  // inbound is treated as outbound, which is the safer default for a list: an
  // unknown row reads as something this node did rather than as correspondence
  // it received.
  function outbound(m) {
    return !(m && m.direction === "in");
  }

  // sortNewestFirst orders a list for display. The API already returns messages
  // newest-first; this exists so a list merged from a refresh and a live poke
  // cannot end up in arrival order, and so ties break stably on id — two messages
  // in the same millisecond must not swap places between renders.
  function sortNewestFirst(list) {
    return (Array.isArray(list) ? list.slice() : []).sort(function (a, b) {
      const ta = Date.parse((a && a.created_at) || "") || 0;
      const tb = Date.parse((b && b.created_at) || "") || 0;
      if (ta !== tb) return tb - ta;
      return ((b && b.id) || 0) - ((a && a.id) || 0);
    });
  }

  // merge folds freshly-fetched messages into what is on screen, keeping one row
  // per id and preferring the newer version of it. A message's state moves after
  // it is first rendered, so a merge that kept the first copy would leave a sent
  // message reading "queued" until the page was reloaded.
  function merge(existing, incoming) {
    const byID = new Map();
    for (const m of Array.isArray(existing) ? existing : []) {
      if (m && m.id != null) byID.set(m.id, m);
    }
    for (const m of Array.isArray(incoming) ? incoming : []) {
      if (m && m.id != null) byID.set(m.id, m);
    }
    return sortNewestFirst(Array.from(byID.values()));
  }

  // isMessageEvent reports whether a hub event is worth re-reading the list for.
  // The event stream is a poke, not the data: the event deliberately carries no
  // message text, so a client reacts by fetching rather than by rendering it.
  function isMessageEvent(e) {
    return !!e && (e.type === "message_out" || e.type === "message_in");
  }

  return {
    MAX_UNITS: MAX_UNITS,
    QUEUED: QUEUED,
    TRANSMITTING: TRANSMITTING,
    SENT: SENT,
    RECEIVED: RECEIVED,
    FAILED: FAILED,
    units: units,
    budget: budget,
    sendable: sendable,
    validID: validID,
    stateClass: stateClass,
    stateMark: stateMark,
    stateKey: stateKey,
    outbound: outbound,
    sortNewestFirst: sortNewestFirst,
    merge: merge,
    isMessageEvent: isMessageEvent,
  };
});
