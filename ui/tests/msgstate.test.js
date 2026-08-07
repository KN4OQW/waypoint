// Unit tests for the messages page's logic (msgstate.js). Run with the Node
// built-in test runner — `node --test` from ui/tests — matching the repo's
// no-build-step, vanilla-JS convention, as linkstate.test.js does.
//
// The DOM controller (messages.js) is a thin wrapper over exactly these
// functions and is validated by hand against the running dashboard.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const WPMsg = require("../static/msgstate.js");

// The length budget is in UTF-16 code units because that is what the on-air
// format carries. Counting CHARACTERS would let an operator type 123 emoji, be
// told they were fine, and get a 413 back from a form that had said yes.
test("the budget counts code units, not characters", () => {
  assert.equal(WPMsg.units("hello"), 5);
  assert.equal(WPMsg.units(""), 0);

  // One emoji is one character and two code units.
  assert.equal([..."👍"].length, 1);
  assert.equal(WPMsg.units("👍"), 2);

  // Accented Latin stays inside the BMP and costs one apiece.
  assert.equal(WPMsg.units("café"), 4);
});

test("the budget reports what is left, and when to say so", () => {
  const empty = WPMsg.budget("");
  assert.equal(empty.used, 0);
  assert.equal(empty.remaining, WPMsg.MAX_UNITS);
  assert.equal(empty.over, false);
  assert.equal(empty.near, false);

  const full = WPMsg.budget("A".repeat(WPMsg.MAX_UNITS));
  assert.equal(full.remaining, 0);
  assert.equal(full.over, false, "exactly at the limit fits");
  assert.equal(full.near, true);

  const over = WPMsg.budget("A".repeat(WPMsg.MAX_UNITS + 1));
  assert.equal(over.remaining, -1);
  assert.equal(over.over, true);
  assert.equal(over.near, false, "over is not near; it is over");

  // 62 emoji are 124 units: over the limit despite being only 62 characters.
  assert.equal(WPMsg.budget("👍".repeat(62)).over, true);
  assert.equal(WPMsg.budget("👍".repeat(61)).over, false);
});

test("a missing or non-string body is empty, not an exception", () => {
  for (const bad of [undefined, null, 42, {}]) {
    assert.equal(WPMsg.units(bad), 0);
    assert.doesNotThrow(() => WPMsg.budget(bad));
  }
});

// A typo must not become a message to a stranger. parseInt("3180202abc") returns
// 3180202 quite happily, which is exactly the failure this guards.
test("validID accepts a 24-bit id and nothing else", () => {
  for (const good of [1, 3180202, 0xFFFFFF, "3180202", " 3180202 "]) {
    assert.equal(WPMsg.validID(good), true, `${good} should be valid`);
  }
  for (const bad of [0, -1, 0x1000000, 1.5, "", "abc", "3180202abc", "31 80202",
                     "0x1234", null, undefined, {}, NaN]) {
    assert.equal(WPMsg.validID(bad), false, `${JSON.stringify(bad)} should be invalid`);
  }
});

test("sendable says why not, as a translation key", () => {
  assert.deepEqual(WPMsg.sendable("3180299", "hi"), { ok: true, reason: "" });
  assert.equal(WPMsg.sendable("", "hi").reason, "messages.err.id");
  assert.equal(WPMsg.sendable("nope", "hi").reason, "messages.err.id");
  assert.equal(WPMsg.sendable("3180299", "").reason, "messages.err.empty");
  assert.equal(WPMsg.sendable("3180299", "A".repeat(200)).reason, "messages.err.long");
  // The id is checked first: with both wrong, the more fundamental one is named.
  assert.equal(WPMsg.sendable("", "").reason, "messages.err.id");
});

// Three outcomes worth distinguishing, and they are not up/down. Colour is never
// the only carrier of meaning here, so each also gets its own mark.
test("state renders as in-flight, done, or failed", () => {
  const cases = [
    { state: "queued", cls: "pending" },
    { state: "transmitting", cls: "pending" },
    { state: "sent", cls: "done" },
    { state: "received", cls: "done" },
    { state: "failed", cls: "failed" },
  ];
  const marks = new Set();
  for (const c of cases) {
    assert.equal(WPMsg.stateClass({ state: c.state }), c.cls, c.state);
    assert.equal(WPMsg.stateKey({ state: c.state }), "messages.state." + c.state);
    marks.add(WPMsg.stateMark({ state: c.state }));
  }
  assert.equal(marks.size, cases.length, "every state needs its own glyph, not just its own colour");
});

test("an unknown or missing state does not throw and does not read as success", () => {
  for (const bad of [undefined, null, {}, { state: 42 }, { state: "delivered" }]) {
    assert.doesNotThrow(() => WPMsg.stateClass(bad));
    assert.equal(WPMsg.stateClass(bad), "pending");
    assert.equal(WPMsg.stateKey(bad), "messages.state.unknown");
  }
  // "delivered" in particular: the protocol cannot know it, so nothing may render
  // it as a success if a future field ever claims it.
  assert.notEqual(WPMsg.stateClass({ state: "delivered" }), "done");
});

test("direction defaults to outbound for a row it cannot read", () => {
  assert.equal(WPMsg.outbound({ direction: "out" }), true);
  assert.equal(WPMsg.outbound({ direction: "in" }), false);
  // An unreadable row reads as something this node did, not as correspondence it
  // received — the safer of the two mistakes.
  for (const bad of [undefined, null, {}, { direction: "" }]) {
    assert.equal(WPMsg.outbound(bad), true);
  }
});

test("sorting is newest-first and stable within a timestamp", () => {
  const same = "2026-08-07T12:00:00.000Z";
  const list = [
    { id: 1, created_at: same },
    { id: 3, created_at: same },
    { id: 2, created_at: "2026-08-07T12:00:01.000Z" },
  ];
  const got = WPMsg.sortNewestFirst(list).map((m) => m.id);
  assert.deepEqual(got, [2, 3, 1]);

  // Repeated sorts of the same input agree: two messages in one millisecond must
  // not swap places between renders.
  for (let i = 0; i < 5; i++) {
    assert.deepEqual(WPMsg.sortNewestFirst(list).map((m) => m.id), got);
  }
  assert.deepEqual(WPMsg.sortNewestFirst(null), []);
});

// A message's state moves after it is first rendered. A merge that kept the first
// copy would leave a sent message reading "queued" until the page was reloaded.
test("merging prefers the fresher copy of a message", () => {
  const before = [{ id: 7, state: "queued", created_at: "2026-08-07T12:00:00.000Z" }];
  const after = [{ id: 7, state: "sent", created_at: "2026-08-07T12:00:00.000Z" }];

  const merged = WPMsg.merge(before, after);
  assert.equal(merged.length, 1, "the same id must not appear twice");
  assert.equal(merged[0].state, "sent");

  // And a genuinely new message joins in the right place.
  const withNew = WPMsg.merge(merged, [{ id: 8, state: "received", created_at: "2026-08-07T12:00:05.000Z" }]);
  assert.deepEqual(withNew.map((m) => m.id), [8, 7]);

  assert.deepEqual(WPMsg.merge(null, null), []);
  // Rows with no id are dropped rather than colliding with each other.
  assert.deepEqual(WPMsg.merge([{ state: "sent" }], []), []);
});

// The event stream is a poke, not the data — the event deliberately carries no
// message text, so a client reacts by fetching rather than by rendering it.
test("only message events trigger a re-read", () => {
  assert.equal(WPMsg.isMessageEvent({ type: "message_out" }), true);
  assert.equal(WPMsg.isMessageEvent({ type: "message_in" }), true);
  for (const other of [{ type: "rf_voice_start" }, { type: "link_up" }, {}, null, undefined]) {
    assert.equal(WPMsg.isMessageEvent(other), false);
  }
});
