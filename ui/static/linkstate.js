/* Waypoint link state: the three-state read of a status Link.

   `state` ("up" | "down" | "unknown") is authoritative. `up` is the legacy
   boolean that predates it and CANNOT express "we have not heard" — it reports
   true for an unknown link, so branching on it renders an unconfirmed link as a
   confident green tick. That is the specific lie this module exists to stop.

   Pure functions, no DOM, no globals, so ui/tests/linkstate.test.js drives them
   directly. Plain script in the same no-build style as app.js/tzpicker.js: it
   attaches a WPLink global for the browser and also exports for CommonJS so the
   Node test runner can require it. */
"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api; // node --test
  if (root) root.WPLink = api;                                              // browser
})(typeof window !== "undefined" ? window : null, function () {
  const UP = "up";
  const DOWN = "down";
  const UNKNOWN = "unknown";

  // linkState reads the authoritative field, falling back to the boolean only for
  // payloads from a daemon old enough not to send one. That fallback can never
  // produce "unknown" — which is correct: a daemon that does not know the state
  // exists was not tracking it either.
  function linkState(l) {
    if (l && typeof l.state === "string" && l.state) return l.state;
    return l && l.up ? UP : DOWN;
  }

  // linkClass drives the row's colour. Unknown gets its own class rather than
  // borrowing "down": an unconfirmed link is not an outage, and reporting one is
  // how an operator learns to distrust the panel.
  function linkClass(l) {
    const s = linkState(l);
    if (s === UP) return "";
    return s === UNKNOWN ? "unknown" : "down";
  }

  // linkMark is the glyph beside the text. Colour is never the only carrier of
  // state in this dashboard (see index.html), so unknown needs its own mark too.
  function linkMark(l) {
    const s = linkState(l);
    if (s === UP) return "✓";
    return s === UNKNOWN ? "?" : "✗";
  }

  // linkWord is the fallback wording when the daemon sent no detail prose.
  function linkWord(l) {
    const s = linkState(l);
    if (s === UP) return "linked";
    return s === UNKNOWN ? "unknown" : "not linked";
  }

  return {
    UP: UP,
    DOWN: DOWN,
    UNKNOWN: UNKNOWN,
    linkState: linkState,
    linkClass: linkClass,
    linkMark: linkMark,
    linkWord: linkWord,
  };
});
