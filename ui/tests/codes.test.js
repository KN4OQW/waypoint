// Unit tests for the fixed-domain code option lists (codes.js) behind the DMR
// colour code and M17 CAN dropdowns. Run with the Node built-in test runner —
// `node --test` from ui/tests — matching the repo's no-build-step convention.
//
// The list itself is not the interesting part. The two cases that are: a blank
// store value, which must show the code the node actually transmits rather than
// nothing, and a stored value outside the domain, which must survive being
// rendered. Both were live bugs elsewhere — see the reach-card regression in
// internal/publicview/node_test.go for the first, and mode_readiness.go's colour
// code rule for what the second costs on the air.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const WPCodes = require("../static/codes.js");

const MAX = WPCodes.FOUR_BIT_MAX;

test("a four-bit code offers exactly 0-15", () => {
  const { options } = WPCodes.codeOptions("1", MAX, "1");
  assert.equal(options.length, 16);
  assert.equal(options[0], "0");
  assert.equal(options[15], "15");
  // Strings throughout: the store holds these as strings and the caller compares
  // them to an <option value> without coercing.
  options.forEach((v) => assert.equal(typeof v, "string"));
});

test("a stored code selects itself and nothing else", () => {
  const { options, selected } = WPCodes.codeOptions("7", MAX, "1");
  assert.equal(selected, "7");
  assert.equal(options.length, 16); // no extra option invented
  assert.ok(options.includes(selected));
});

test("a blank code shows what the config renders as, not an empty option", () => {
  // render.go defaults a blank [DMR] ColorCode to 1 and a blank [M17] CAN to 0.
  // The panel reports the effective code because that is the one on the air.
  for (const blank of ["", "   ", null, undefined]) {
    assert.equal(WPCodes.codeOptions(blank, MAX, "1").selected, "1");
    assert.equal(WPCodes.codeOptions(blank, MAX, "0").selected, "0");
  }
  // And the fallback is a member of the domain, so nothing is prepended for it.
  assert.equal(WPCodes.codeOptions("", MAX, "1").options.length, 16);
});

test("an out-of-range stored code is kept, selected, and out of numeric order", () => {
  // An imported configuration can carry colour code 20. MMDVM-Host transmits it
  // as 4 and says nothing; a dropdown that quietly displayed 1 would tell the
  // operator the node is fine while every radio fails to decode.
  const { options, selected } = WPCodes.codeOptions("20", MAX, "1");
  assert.equal(selected, "20");
  assert.equal(options[0], "20", "an out-of-domain value belongs at the front, not between 2 and 3");
  assert.equal(options.length, 17);
  assert.equal(options.filter((v) => v === "20").length, 1);
});

test("a non-numeric stored code is kept rather than silently corrected", () => {
  // atoi("one") is 0, so this node runs on colour code 0. Showing 0 as though the
  // operator had chosen it would hide the typo that got them there; the findings
  // card under the panel is what explains it.
  const { options, selected } = WPCodes.codeOptions("one", MAX, "1");
  assert.equal(selected, "one");
  assert.equal(options[0], "one");
  assert.equal(options.length, 17);
});

test("surrounding whitespace is not a distinct code", () => {
  const { options, selected } = WPCodes.codeOptions(" 7 ", MAX, "1");
  assert.equal(selected, "7");
  assert.equal(options.length, 16, "a padded value is the same code, not a new option");
});

test("the selected value is always present in the options", () => {
  for (const cur of ["", "0", "15", "16", "-1", "one", "  ", "1.5"]) {
    const { options, selected } = WPCodes.codeOptions(cur, MAX, "1");
    assert.ok(options.includes(selected), `selected ${JSON.stringify(selected)} is missing from the options`);
  }
});
