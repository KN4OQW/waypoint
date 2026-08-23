// Unit tests for the timezone picker logic (issue #139). Run with the Node
// built-in test runner — `node --test` from ui/tests — so the dashboard gets a
// test gate without pulling in a framework, matching the repo's no-build-step,
// vanilla-JS convention. These drive the PURE functions in ../static/tzpicker.js;
// the DOM component (createTzPicker) is validated by hand against the running
// dashboard (see the PR's validation notes) and is a thin wrapper over exactly
// these functions.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const WPTz = require("../static/tzpicker.js");

// A small, representative slice of `timedatectl list-timezones`.
const ZONES = [
  "America/Chicago",
  "America/New_York",
  "America/Los_Angeles",
  "Asia/Kolkata",
  "Europe/London",
  "Europe/Paris",
  "UTC",
];

test("matchDetectedZone: detected present in list returns it (exact, case-sensitive)", () => {
  assert.equal(WPTz.matchDetectedZone("America/New_York", ZONES), "America/New_York");
  assert.equal(WPTz.matchDetectedZone("UTC", ZONES), "UTC");
});

test("matchDetectedZone: absent from list falls back to null", () => {
  // Alias drift: the browser's Asia/Calcutta is not in a modern tzdata list.
  assert.equal(WPTz.matchDetectedZone("Asia/Calcutta", ZONES), null);
  // Case must match exactly — no fuzzy matching in v1 (D2).
  assert.equal(WPTz.matchDetectedZone("america/new_york", ZONES), null);
});

test("matchDetectedZone: null/undefined detection falls back to null", () => {
  assert.equal(WPTz.matchDetectedZone(null, ZONES), null);
  assert.equal(WPTz.matchDetectedZone(undefined, ZONES), null);
  assert.equal(WPTz.matchDetectedZone("", ZONES), null);
});

test("matchDetectedZone: a failed list fetch (empty/non-array) yields no match", () => {
  assert.equal(WPTz.matchDetectedZone("America/New_York", []), null);
  assert.equal(WPTz.matchDetectedZone("America/New_York", null), null);
  assert.equal(WPTz.matchDetectedZone("America/New_York", undefined), null);
});

test("tzSuggestion: no prior value prefills the detected zone (D3)", () => {
  assert.deepEqual(WPTz.tzSuggestion("Europe/London", ZONES, ""), { kind: "prefill", zone: "Europe/London" });
  assert.deepEqual(WPTz.tzSuggestion("Europe/London", ZONES, "   "), { kind: "prefill", zone: "Europe/London" });
});

test("tzSuggestion: configured zone differing from detected offers a hint (D4)", () => {
  assert.deepEqual(WPTz.tzSuggestion("Europe/London", ZONES, "America/Chicago"), { kind: "hint", zone: "Europe/London" });
});

test("tzSuggestion: configured zone equal to detected offers nothing (D4 no-op)", () => {
  assert.deepEqual(WPTz.tzSuggestion("Europe/London", ZONES, "Europe/London"), { kind: "none", zone: "Europe/London" });
});

test("tzSuggestion: unmatched detection offers nothing regardless of configured (D2 fallback)", () => {
  assert.deepEqual(WPTz.tzSuggestion("Asia/Calcutta", ZONES, ""), { kind: "none", zone: null });
  assert.deepEqual(WPTz.tzSuggestion(null, ZONES, "America/Chicago"), { kind: "none", zone: null });
});

test("filterZones: case-insensitive substring match", () => {
  assert.deepEqual(WPTz.filterZones("paris", ZONES), ["Europe/Paris"]);
  assert.deepEqual(WPTz.filterZones("EUROPE", ZONES), ["Europe/London", "Europe/Paris"]);
});

test("filterZones: underscore is treated as a space, either way round", () => {
  // "new york" (with a space) matches America/New_York (with an underscore).
  assert.deepEqual(WPTz.filterZones("new york", ZONES), ["America/New_York"]);
  // and an underscore in the query matches too.
  assert.deepEqual(WPTz.filterZones("new_york", ZONES), ["America/New_York"]);
  assert.deepEqual(WPTz.filterZones("los angeles", ZONES), ["America/Los_Angeles"]);
});

test("filterZones: empty query returns the whole list; no match returns nothing", () => {
  assert.deepEqual(WPTz.filterZones("", ZONES), ZONES);
  assert.deepEqual(WPTz.filterZones("   ", ZONES), ZONES);
  assert.deepEqual(WPTz.filterZones("zzzznope", ZONES), []);
});

test("filterZones: a missing/empty list never throws", () => {
  assert.deepEqual(WPTz.filterZones("paris", null), []);
  assert.deepEqual(WPTz.filterZones("paris", undefined), []);
});

// tzKeyAction returns {active, open, commit} without `count` — the component
// re-derives count from the current matches on every keystroke. press() mirrors
// that: it feeds the persistent count back in while threading the reducer's
// active/open forward, so these tests exercise real multi-key sequences.
function press(count, state, key) {
  const next = WPTz.tzKeyAction({ count: count, active: state.active, open: state.open }, key);
  return { active: next.active, open: next.open, commit: next.commit };
}

test("tzKeyAction: ArrowDown opens a closed list and moves the active option, clamping at the end", () => {
  let s = { active: -1, open: false };
  s = press(3, s, "ArrowDown");
  assert.deepEqual(s, { active: 0, open: true, commit: -1 });
  s = press(3, s, "ArrowDown");
  assert.deepEqual(s, { active: 1, open: true, commit: -1 });
  s = press(3, s, "ArrowDown");
  s = press(3, s, "ArrowDown"); // clamps, does not wrap
  assert.deepEqual(s, { active: 2, open: true, commit: -1 });
});

test("tzKeyAction: ArrowUp clamps at the first option", () => {
  let s = { active: 1, open: true };
  s = press(3, s, "ArrowUp");
  assert.deepEqual(s, { active: 0, open: true, commit: -1 });
  s = press(3, s, "ArrowUp"); // clamps at 0
  assert.deepEqual(s, { active: 0, open: true, commit: -1 });
});

test("tzKeyAction: Home/End jump to the ends", () => {
  assert.deepEqual(WPTz.tzKeyAction({ count: 5, active: 2, open: true }, "Home"), { active: 0, open: true, commit: -1 });
  assert.deepEqual(WPTz.tzKeyAction({ count: 5, active: 2, open: true }, "End"), { active: 4, open: true, commit: -1 });
});

test("tzKeyAction: Enter on an active option commits it and closes", () => {
  const s = WPTz.tzKeyAction({ count: 3, active: 1, open: true }, "Enter");
  assert.equal(s.commit, 1);
  assert.equal(s.open, false);
});

test("tzKeyAction: Enter with nothing active selects nothing (no-match keyboard case, D6)", () => {
  // A gibberish filter yields no matches (count 0), so Enter can never commit.
  assert.deepEqual(WPTz.tzKeyAction({ count: 0, active: -1, open: true }, "Enter"), { active: -1, open: true, commit: -1 });
  // Likewise a matched list with no highlighted option.
  assert.equal(WPTz.tzKeyAction({ count: 3, active: -1, open: true }, "Enter").commit, -1);
});

test("tzKeyAction: arrows on an empty result stay closed and commit nothing", () => {
  assert.deepEqual(WPTz.tzKeyAction({ count: 0, active: -1, open: false }, "ArrowDown"), { active: -1, open: false, commit: -1 });
});

test("tzKeyAction: Escape closes and clears the active option", () => {
  assert.deepEqual(WPTz.tzKeyAction({ count: 3, active: 2, open: true }, "Escape"), { active: -1, open: false, commit: -1 });
});

test("detectTimezone: returns a non-empty IANA string on a normal runtime", () => {
  const tz = WPTz.detectTimezone();
  // Node exposes Intl, so this resolves to the host zone (a non-empty string).
  assert.equal(typeof tz, "string");
  assert.ok(tz.length > 0);
});

test("detectTimezone: returns null when the runtime throws or reports nothing", () => {
  const realIntl = global.Intl;
  try {
    // Old/broken runtime: resolvedOptions().timeZone is undefined.
    global.Intl = { DateTimeFormat: function () { return { resolvedOptions: function () { return {}; } }; } };
    assert.equal(WPTz.detectTimezone(), null);
    // Runtime that throws outright.
    global.Intl = { DateTimeFormat: function () { throw new Error("no Intl"); } };
    assert.equal(WPTz.detectTimezone(), null);
  } finally {
    global.Intl = realIntl;
  }
});

// --- the item shape the county picker added ---------------------------------
//
// createTzPicker was generalized rather than copied so the county picker and the
// timezone picker share one ARIA implementation (see the file header). These
// cover the pure half of that generalization; the DOM component is still checked
// by hand and by the axe job, as it was before.

test("toItems: a plain string is its own value and label", () => {
  assert.deepEqual(WPTz.toItems(["America/Chicago"]), [
    { value: "America/Chicago", label: "America/Chicago", sub: "", data: undefined },
  ]);
});

test("toItems: an object keeps value, label and sub", () => {
  assert.deepEqual(WPTz.toItems([{ value: "012113", label: "Santa Rosa, FL", sub: "012113" }]), [
    { value: "012113", label: "Santa Rosa, FL", sub: "012113", data: undefined },
  ]);
});

// The county picker has to store five fields when a row is chosen, so whatever a
// caller hangs on `data` must survive normalization untouched. Re-fetching the
// row it just rendered to get those fields back would be the picker asking the
// server what it already knows.
test("toItems: data rides along untouched", () => {
  const county = { same: "012113", ugc: "FLC113", name: "Santa Rosa", state: "FL", wfo: "MOB" };
  const got = WPTz.toItems([{ value: "012113", label: "Santa Rosa, FL", data: county }]);
  assert.equal(got[0].data, county);
});

// A label is what the operator reads. An item that arrived without one must fall
// back to the value rather than render an empty row they cannot tell apart from
// the next empty row.
test("toItems: a missing label falls back to the value", () => {
  assert.deepEqual(WPTz.toItems([{ value: "012113" }]), [
    { value: "012113", label: "012113", sub: "", data: undefined },
  ]);
});

test("toItems: junk entries normalize to empty rather than throwing", () => {
  assert.deepEqual(WPTz.toItems([null, undefined, 7]), [
    { value: "", label: "", sub: "", data: undefined },
    { value: "", label: "", sub: "", data: undefined },
    { value: "", label: "", sub: "", data: undefined },
  ]);
  assert.deepEqual(WPTz.toItems(null), []);
});

test("filterItems: matches the label, the value and the sub", () => {
  const items = WPTz.toItems([
    { value: "012113", label: "Santa Rosa, FL", sub: "012113" },
    { value: "012033", label: "Escambia, FL", sub: "012033" },
  ]);
  assert.equal(WPTz.filterItems("santa", items).length, 1);
  // Reachable by the code as well as the name, since an operator migrating from
  // a hand-written configuration already knows the code.
  assert.equal(WPTz.filterItems("012033", items).length, 1);
  assert.equal(WPTz.filterItems("fl", items).length, 2);
});

test("filterItems: an empty query returns everything, and a miss returns nothing", () => {
  const items = WPTz.toItems([{ value: "a", label: "Alpha" }, { value: "b", label: "Beta" }]);
  assert.equal(WPTz.filterItems("   ", items).length, 2);
  assert.equal(WPTz.filterItems("qqzz", items).length, 0);
});
