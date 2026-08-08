/* Waypoint numeric codes with a fixed, tiny domain — DMR's colour code and M17's
   CAN, both four-bit fields.

   These were free-text inputs, and that is worse than it looks. Nothing in the
   path range-checks them: MMDVM-Host reads both with a bare atoi (Conf.cpp:810-811
   for ColorCode), and the value is then masked into the wire format —
   DMRSlotType.cpp:62 writes (code << 4) & 0xF0, M17LSF.cpp:141-148 splits the CAN
   across two LSF bytes. So colour code 20 is accepted at every layer and
   transmitted as 4, with no error anywhere and no radio able to decode. The
   readiness rules in internal/config/mode_readiness.go report that after the fact;
   a dropdown means it cannot be typed in the first place.

   Two behaviours here are not obvious and are the reason this is a module with
   tests rather than a loop inlined in the panel:

   - A blank store value shows the *effective* code, not an empty option.
     render.go defaults a blank ColorCode to 1 and a blank CAN to 0, so the node
     is demonstrably running on that code. Showing nothing would be a smaller
     answer than the truth — the same reasoning as the reach card in
     internal/publicview/node.go, which had this exact bug.

   - A stored value outside the domain is kept as an option and stays selected.
     Imported configurations carry whatever the previous host had, and a select
     that silently displayed 1 while the store held 20 would tell the operator
     their node is fine while it transmits 4. The out-of-range option renders as
     the bare value; the findings card under the panel is what explains it, in
     the operator's language rather than a suffix bolted onto an option label.

   Pure functions, no DOM, no globals, so ui/tests/codes.test.js drives them
   directly. Plain script in the same no-build style as linkstate.js: it attaches
   a WPCodes global for the browser and also exports for CommonJS so the Node
   test runner can require it. */
"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api; // node --test
  if (root) root.WPCodes = api;                                              // browser
})(typeof window !== "undefined" ? window : null, function () {
  // FOUR_BIT_MAX is the largest value a four-bit code can carry. Both the DMR
  // colour code and the M17 CAN are four-bit fields.
  const FOUR_BIT_MAX = 15;

  // codeOptions builds the option list for a fixed-domain numeric code.
  //
  //   cur       the stored value, as the store holds it (a string, possibly
  //             blank, possibly not even a number)
  //   max       the top of the domain; the list runs 0..max
  //   fallback  what a blank value renders as in the generated config
  //
  // Returns {options, selected} — plain strings, so the caller escapes and marks
  // up. `selected` is always present in `options`.
  function codeOptions(cur, max, fallback) {
    const options = [];
    for (let i = 0; i <= max; i++) options.push(String(i));

    const trimmed = String(cur == null ? "" : cur).trim();
    const selected = trimmed === "" ? String(fallback) : trimmed;

    // An out-of-domain value goes at the front rather than in numeric position:
    // it is not part of the domain, and burying 20 between 2 and 3 would read as
    // though it were.
    if (options.indexOf(selected) < 0) options.unshift(selected);

    return { options: options, selected: selected };
  }

  return {
    FOUR_BIT_MAX: FOUR_BIT_MAX,
    codeOptions: codeOptions,
  };
});
