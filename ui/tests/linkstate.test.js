// Unit tests for the dashboard's three-state link read (linkstate.js). Run with
// the Node built-in test runner — `node --test` from ui/tests — matching the
// repo's no-build-step, vanilla-JS convention.
//
// The case that matters here is "unknown". It did not exist before the session
// tri-state landed, and every piece of legacy boolean logic mishandles it: `up`
// is true for an unknown link, so anything branching on the boolean paints an
// unconfirmed link as a confident green tick. These tests pin that it renders as
// its own thing — not up, and equally not down.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const WPLink = require("../static/linkstate.js");

test("state is authoritative over the legacy boolean", () => {
  // The exact payload waypointd emits for a link nothing has vouched for: up is
  // true (do not act) while state says we have not heard.
  const unknown = { up: true, state: "unknown", detail: "awaiting session evidence" };
  assert.equal(WPLink.linkState(unknown), "unknown");
  assert.equal(WPLink.linkClass(unknown), "unknown");
  assert.equal(WPLink.linkMark(unknown), "?");
  assert.equal(WPLink.linkWord(unknown), "unknown");
});

test("unknown is neither up nor down", () => {
  const unknown = { up: true, state: "unknown" };
  const up = { up: true, state: "up" };
  const down = { up: false, state: "down" };

  // Not painted as healthy...
  assert.notEqual(WPLink.linkClass(unknown), WPLink.linkClass(up));
  assert.notEqual(WPLink.linkMark(unknown), WPLink.linkMark(up));
  // ...and not reported as an outage either.
  assert.notEqual(WPLink.linkClass(unknown), WPLink.linkClass(down));
  assert.notEqual(WPLink.linkMark(unknown), WPLink.linkMark(down));
});

test("evidenced states render as before", () => {
  const up = { up: true, state: "up", detail: "logged in" };
  assert.equal(WPLink.linkClass(up), ""); // no modifier class: the healthy row
  assert.equal(WPLink.linkMark(up), "✓");
  assert.equal(WPLink.linkWord(up), "linked");

  const down = { up: false, state: "down", detail: "not logged in" };
  assert.equal(WPLink.linkClass(down), "down");
  assert.equal(WPLink.linkMark(down), "✗");
  assert.equal(WPLink.linkWord(down), "not linked");
});

test("a payload with no state falls back to the boolean", () => {
  // A dashboard talking to a daemon from before the field existed. The fallback
  // can never yield "unknown", which is right: that daemon was not tracking it.
  assert.equal(WPLink.linkState({ up: true }), "up");
  assert.equal(WPLink.linkState({ up: false }), "down");
  assert.equal(WPLink.linkState({ up: true, state: "" }), "up");
});

test("a missing or malformed link does not throw", () => {
  // renderNetworks maps over whatever /api/status returned; a null row must not
  // take the whole dashboard down with it.
  for (const bad of [undefined, null, {}, { state: 42 }]) {
    assert.doesNotThrow(() => WPLink.linkClass(bad));
    assert.equal(WPLink.linkState(bad), "down"); // no evidence of up
  }
});
