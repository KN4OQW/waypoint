// Automated accessibility gate for the Waypoint UI.
//
// Loads the dashboard and every settings/wizard panel in a real headless Chromium
// and runs axe-core against each, in all three display themes. Any WCAG 2.1
// A/AA violation fails the process (non-zero exit) so CI can gate merges on it.
//
// It drives the *running daemon* (waypointd -demo), not the raw static files, so
// the panels render with live data exactly as an operator sees them.
//
// The nav has two topologies (RFC-0009), and both are walked: a grouped collapsible
// sidebar at >=1024px and a tile grid below it. Every panel is therefore scanned
// twice per theme — once per viewport — plus the nav's own states: groups expanded,
// groups collapsed, and the mobile tile grid.
//
// The daemon gates the whole UI behind the RFC-0002 claim: an unclaimed device
// answers every page with `{"error":"device is unclaimed"}`, and axe then scans that
// JSON blob instead of the app. Each context therefore claims the node (or logs in,
// if a previous run already claimed it) before scanning anything.
//
// Both display modes are walked as well as both nav topologies. Dark is the
// product default (RFC-0009), but the mode is *not* left to the browser: see
// the note on the context below for why picking it explicitly matters.
//
// Env:
//   BASE                 base URL of a running `waypointd -demo` (default http://127.0.0.1:8073)
//   PLAYWRIGHT_CHROMIUM  explicit Chromium binary (optional; omit to use Playwright's own)
//   A11Y_THEMES          comma list of themes to test (default phosphor,amber,ice)
//   A11Y_MODES           comma list of display modes to test (default dark,light)
//   A11Y_USER/A11Y_PASS  claim/login credentials for the demo node (defaults below)

import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";

const BASE = process.env.BASE || "http://127.0.0.1:8073";
const THEMES = (process.env.A11Y_THEMES || "phosphor,amber,ice").split(",").map((s) => s.trim()).filter(Boolean);
const MODES = (process.env.A11Y_MODES || "dark,light").split(",").map((s) => s.trim()).filter(Boolean);
const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];
const USER = process.env.A11Y_USER || "a11y";
const PASS = process.env.A11Y_PASS || "a11y-scan-passphrase";

// Every top-level settings tab (mirrors the TABS list in settings.js).
const TABS = [
  "general", "hardware", "setup", "lcd", "station", "modes",
  "brandmeister", "network", "gateways",
  "profiles", "phonebook", "notify", "updates", "system", "expert",
];

// The Modes tab's sub-tabs (mirrors MODE_SUBS in settings.js). These render the
// eight per-mode panels, which are no longer top-level tabs.
const MODE_SUBS = ["dstar", "dmr", "ysf", "p25", "nxdn", "m17", "pocsag", "fm"];

// Desktop is the grouped sidebar; mobile sits below the 1024px breakpoint, where the
// nav becomes the tile grid.
const VIEWPORTS = [
  { key: "desktop", size: { width: 1280, height: 900 } },
  { key: "mobile", size: { width: 390, height: 844 } },
];

// Each top-level tab, with the Modes tab expanded into one target per sub-tab, so
// axe walks every rendered panel exactly as before the consolidation.
const TARGETS = TABS.flatMap((t) => (t === "modes" ? MODE_SUBS.map((s) => ({ tab: t, sub: s })) : [{ tab: t }]));

const launchOpts = {};
if (process.env.PLAYWRIGHT_CHROMIUM) launchOpts.executablePath = process.env.PLAYWRIGHT_CHROMIUM;

const browser = await chromium.launch(launchOpts);
let violations = 0;
let scans = 0;

// Set per context, and prefixed onto every scan label so a failure line names the
// theme and mode it was measured under. Without it a report of "settings#general
// — color-contrast" is unactionable: the same panel passes in one palette and
// fails in another.
let ctxLabel = "";

function report(label, result) {
  label = `${ctxLabel} ${label}`;
  scans++;
  const v = result.violations;
  if (!v.length) {
    console.log(`  ok   ${label}`);
    return;
  }
  violations += v.length;
  console.log(`  FAIL ${label} — ${v.length} violation(s)`);
  for (const x of v) {
    console.log(`       [${x.impact}] ${x.id}: ${x.help}`);
    for (const n of x.nodes.slice(0, 6)) {
      console.log(`         → ${n.target}`);
      console.log(`           ${n.html.replace(/\s+/g, " ").slice(0, 140)}`);
    }
    console.log(`         ${x.helpUrl}`);
  }
}

async function analyze(page, label) {
  // Toggle every off-state control on, so we also exercise the "enabled" accent
  // styling (pills, mode tiles) that the default render leaves off.
  //
  // `.swatch` is excluded, and that exclusion is load-bearing rather than tidy.
  // The theme swatches and the dark/light toggle are all `aria-pressed` buttons
  // in #swatches, so the blanket sweep used to click every one of them. Each
  // swatch's handler writes localStorage("wp-theme"), so the page ended up on
  // whichever swatch happened to be last in the DOM *and* carried that choice
  // into every later page in the context. The theme loop below was therefore
  // decorative: scanning with A11Y_THEMES=phosphor still reported violations
  // coloured with the ice accent #1f77c9. Measured 2026-08-02, on the run that
  // produced the 135-violation figure in #121.
  await page.evaluate(() => {
    document.querySelectorAll('.mode-card.off, .pill.off, [aria-pressed="false"]').forEach((b) => {
      if (b.closest("#swatches")) return;
      if (typeof b.click === "function") b.click();
    });
    // Expand every inline-help block (#135) so its open state is scanned too, not
    // just the collapsed one. Done after the toggles above, which re-render rows.
    document.querySelectorAll('.help-btn[aria-expanded="false"]').forEach((b) => b.click());
  }).catch(() => {});
  await page.waitForTimeout(150);
  const result = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  report(label, result);
}

// Claim the node, or log in when a previous run already claimed it. The request
// goes through the context, so the session cookie lands in the jar the pages use.
// Without this every scan below would be looking at the claim gate's JSON.
async function authenticate(context) {
  const data = { username: USER, password: PASS };
  // An already-claimed node rejects the claim (409 on the race, 401 once the gate
  // has moved on to requiring a login) — either way the fallback is to log in.
  let r = await context.request.post(`${BASE}/api/claim`, { data });
  if (!r.ok()) r = await context.request.post(`${BASE}/api/session`, { data });
  if (!r.ok()) throw new Error(`could not authenticate against ${BASE}: ${r.status()} ${(await r.text()).trim()}`);
}

// Seed the phonebook with entries and logins, so the Phonebook panel is scanned
// POPULATED rather than in its empty state (RFC-0002 Amendment 1).
//
// This matters more than it sounds. An empty phonebook renders one paragraph of
// prose; the populated panel renders the table, the Login column, a role <select>
// per account, a must-rotate pill and the last admin's DISABLED controls with the
// explanation beside them. Every one of those is a thing axe has an opinion about,
// and none of them exist in the empty state — a green scan of the empty panel is a
// green scan of nothing.
//
// Three rows, chosen to produce the three shapes the column can take:
//   W1AW    an operator login, freshly granted, so must_rotate is set (the pill)
//   K2ABC   no login at all (the "no login" cell)
//   N0SZ    an admin login, and since the scan's own account is the other admin,
//           this one is NOT the last admin — so its controls stay live. The
//           disabled-and-explained state is produced separately below, because it
//           needs to be the only admin and the scan account cannot be deleted.
//
// Seeded through the API rather than the UI: the panel reads it back the same way
// either route, and driving four forms per row would make the scan a UI test that
// happens to run axe. Failures are swallowed — a rerun against an already-seeded
// node gets 409s, and the panel is populated either way.
async function seedPhonebook(context) {
  const entries = [
    { callsign: "W1AW", dmr_id: 3100001, full_name: "Hiram Percy Maxim", email: "w1aw@example.org" },
    { callsign: "K2ABC", dmr_id: 3100002, full_name: "Pat Operator" },
    { callsign: "N0SZ", dmr_id: 3101901, full_name: "Rocky" },
  ];
  const ids = {};
  for (const e of entries) {
    const r = await context.request.post(`${BASE}/api/phonebook`, { data: e });
    if (r.ok()) ids[e.callsign] = (await r.json()).id;
  }
  // Look up anything that already existed, so a rerun still links the accounts.
  const list = await context.request.get(`${BASE}/api/phonebook`);
  if (list.ok()) {
    for (const e of (await list.json()).entries || []) ids[e.callsign] = e.id;
  }
  // One entry imported through the real route, so the "from the public list"
  // marker is rendered rather than only the hand-typed rows. It needs the demo
  // node to have an ID table; where there is none the import 404s and the panel is
  // scanned without the marker, which is the honest degradation.
  await context.request.post(`${BASE}/api/phonebook/import`, { data: { dmr_id: 3180202 } });
  for (const [callsign, username, role] of [["W1AW", "w1aw", "operator"], ["N0SZ", "n0sz", "admin"]]) {
    if (!ids[callsign]) continue;
    await context.request.post(`${BASE}/api/accounts`, {
      data: {
        username,
        password: "seeded-scan-password",
        role,
        phonebook_id: ids[callsign],
      },
    });
  }
}

// Force the two Phonebook states the seeded data cannot produce on its own.
//
// The grant form replaces the entry form in the second column, so it is never
// rendered by a plain load. The last-admin state needs exactly one admin, and the
// scan is signed in as one — so rather than delete accounts out from under the
// session, the count is faked in the panel's own model and re-rendered. That is
// honest for an accessibility scan: what is being measured is the markup the
// operator meets, and this is that markup.
async function openPhonebookStates(page, analyze, label) {
  // The public-list search, in its two interesting states: results with Add
  // buttons, and the no-match branch whose copy is the longest prose on the panel.
  // Both are driven through the real control rather than by poking state, so the
  // scan sees the markup an operator actually produces.
  await page.fill("#pb-find", "KN4OQW").catch(() => {});
  await page.click('[data-pb-action="find"]').catch(() => {});
  await page.waitForTimeout(400);
  // Two different reasons the results can be empty, and only one is a bug.
  //
  // A node with no ID table cannot produce this state at all — the panel says so
  // instead, which is correct behaviour and is itself scanned below. CI passes a
  // fixture table so the state DOES exist there; a developer running the scan
  // against a node that has never downloaded it gets the skip and a line saying
  // why, rather than a failure about something that is working.
  //
  // A table that IS present and still returns nothing is a regression, and that
  // one throws: the alternative is a green scan of markup nobody looked at.
  const noTable = await page.evaluate(() =>
    !!document.querySelector(".idfind-out, .note") &&
    /public ID database/i.test(document.body.textContent || "")).catch(() => false);
  const gotRows = await page.evaluate(() => !!document.querySelector(".idfind-list li")).catch(() => false);
  if (gotRows) {
    await analyze(page, `${label} settings#phonebook (public-list results)`);
  } else if (noTable) {
    console.log(`  skip ${ctxLabel} ${label} settings#phonebook (public-list results) — this node has no ID table`);
  } else {
    throw new Error("the public-list search returned nothing and the panel did not say why; the scan would have passed without measuring it");
  }

  await page.fill("#pb-find", "ZZ9ZZZ").catch(() => {});
  await page.click('[data-pb-action="find"]').catch(() => {});
  await page.waitForTimeout(400);
  await analyze(page, `${label} settings#phonebook (public-list no match)`);

  await page.evaluate(() => {
    const first = (pb.entries || [])[0];
    if (first) pbBeginGrant(String(first.id));
  }).catch(() => {});
  await page.waitForTimeout(200);
  if (!await page.evaluate(() => !!document.getElementById("pb-acct-pass")).catch(() => false)) {
    throw new Error("the grant form did not render; the scan would have passed without measuring it");
  }
  await analyze(page, `${label} settings#phonebook (grant form)`);

  // The admin kept has to be a LINKED one. The scan's own account is an admin
  // with no phonebook_id — it is created by the claim, which the amendment is
  // explicit must not invent an identity — so keeping "the first admin" kept the
  // one account that renders no row at all, and the state under test never
  // appeared. The scan went green on a panel that did not contain it.
  await page.evaluate(() => {
    pb.granting = null;
    const linkedAdmin = (pb.accounts || []).find((a) => a.role === "admin" && a.phonebook_id);
    pb.accounts = (pb.accounts || []).filter((a) => a.role !== "admin")
      .concat(linkedAdmin ? [linkedAdmin] : []);
    renderPanel();
  }).catch(() => {});
  await page.waitForTimeout(200);
  // Assert the state is really on the page before scanning it. Every evaluate
  // here ends in .catch(() => {}) so a broken selector degrades to the plain
  // panel rather than failing — which is precisely how an accessibility gate
  // reports a pass for markup it never saw.
  const rendered = await page.evaluate(() =>
    document.querySelectorAll("[data-pb-revoke][disabled]").length).catch(() => 0);
  if (!rendered) {
    throw new Error("the last-admin state did not render; the scan would have passed without measuring it");
  }
  await analyze(page, `${label} settings#phonebook (last admin)`);
}

// The forced-rotation screen (RFC-0002 Amendment 1).
//
// It is a self-contained pre-auth page like claim and login, served at "/" to an
// account that still owes a rotation, so it is reached with its OWN context: the
// scan's admin session has nothing to rotate and would be served the dashboard.
//
// A fresh context per call, discarded after: this signs in as somebody else, and
// leaking that session into the panel walk would scan the whole settings surface
// as an account that can reach one route.
async function scanRotationScreen(browser, theme, mode, vp, analyze) {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, colorScheme: mode, viewport: vp.size });
  await ctx.addInitScript(([t, m]) => {
    localStorage.setItem("wp-theme", t);
    localStorage.setItem("wp-mode", m);
  }, [theme, mode]);
  try {
    const r = await ctx.request.post(`${BASE}/api/session`, {
      data: { username: "w1aw", password: "seeded-scan-password" },
    });
    if (!r.ok()) return;               // not seeded, or already rotated
    const p = await ctx.newPage();
    await p.goto(BASE + "/", { waitUntil: "domcontentloaded" });
    await p.waitForTimeout(200);
    // Only scan it if it IS the rotation screen; a rerun against a node whose
    // seeded account has already rotated would otherwise scan the dashboard again
    // under a label claiming it was this screen.
    const isRotate = await p.evaluate(() => !!document.querySelector('input#current'));
    if (isRotate) await analyze(p, `${vp.key} rotation screen`);
    // And its error state, which is the only coloured thing on the page.
    if (isRotate) {
      await p.evaluate(() => {
        const e = document.getElementById("err");
        if (e) { e.textContent = "Passwords do not match"; e.hidden = false; }
      }).catch(() => {});
      await p.waitForTimeout(100);
      await analyze(p, `${vp.key} rotation screen (error)`);
    }
  } finally {
    await ctx.close();
  }
}

// Open a settings target. The canonical deep link for a mode panel is
// "#modes/<sub>"; selectTab is called as well because a hash-only goto is a
// same-document navigation and does not re-run the page script.
async function open(page, tab, sub) {
  const hash = sub ? `${tab}/${sub}` : tab;
  await page.goto(`${BASE}/settings.html#${hash}`, { waitUntil: "domcontentloaded" });
  await page.evaluate(([t, s]) => window.selectTab && window.selectTab(t, s), [tab, sub || ""]).catch(() => {});
  await page.waitForTimeout(250);
}

// Put the General tab's DMR ID lookup (#140) into its answered state and scan
// that, because the default render never shows it: the block only exists after a
// callsign has been looked up, so the ordinary settings#general pass walks past
// the list, its Use buttons and its "in use" marker without seeing any of them.
//
// The state is seeded directly rather than by typing a callsign and pressing the
// button. A real lookup needs a DMRIds.dat on the machine, which CI does not have
// and should not need for an accessibility scan; seeding also pins the awkward
// cases — a row already in use, and a truncated list — that a live table would
// only produce for particular callsigns. If the settings page ever stops carrying
// these globals the evaluate throws, the catch swallows it, and the scan degrades
// to the plain panel rather than failing for the wrong reason.
async function openIDLookup(page) {
  await page.evaluate(() => {
    edit.general = Object.assign({}, edit.general, { callsign: "N0SZ", id: "3101901" });
    idLookup = {
      status: "done",
      callsign: "N0SZ",
      records: [
        { id: 3101900, callsign: "N0SZ", name: "Rocky" },
        { id: 3101901, callsign: "N0SZ", name: "Rocky" },
        { id: 3101902, callsign: "N0SZ", name: "" },
      ],
      truncated: true,
      available: true,
    };
    renderPanel();
  }).catch(() => {});
  await page.waitForTimeout(150);
}

// Drive every sidebar group to the wanted state through its own disclosure button,
// so the scan exercises the real control rather than poking at stored state. Each
// click re-renders the nav; the captured buttons keep working because the handler
// closes over the group name.
async function setGroups(page, expanded) {
  await page.evaluate((want) => {
    document.querySelectorAll(".nav-group-btn").forEach((b) => {
      if ((b.getAttribute("aria-expanded") === "true") !== want) b.click();
    });
  }, expanded).catch(() => {});
  await page.waitForTimeout(150);
}

for (const theme of THEMES) for (const mode of MODES) {
  console.log(`\n=== theme: ${theme}, mode: ${mode} ===`);
  ctxLabel = `[${theme}/${mode}]`;
  // ignoreHTTPSErrors: a real node serves the RFC-0012 self-signed device cert, so
  // pointing BASE at one would otherwise fail the handshake before axe sees a page.
  //
  // colorScheme is pinned rather than left at Playwright's default, which is
  // `light`. index.html and settings.html resolve an unset "wp-mode" through
  // prefers-color-scheme, so the default context silently put every page in
  // light mode — the scan had never once measured the dark palette that is the
  // product default. wp-mode is seeded too, so the result does not depend on
  // which of the two signals the page happens to consult first.
  const context = await browser.newContext({ ignoreHTTPSErrors: true, colorScheme: mode });
  await context.addInitScript(([t, m]) => {
    localStorage.setItem("wp-theme", t);
    localStorage.setItem("wp-mode", m);
  }, [theme, mode]);
  await authenticate(context);
  await seedPhonebook(context);
  const page = await context.newPage();

  for (const vp of VIEWPORTS) {
    console.log(`  -- viewport: ${vp.key} (${vp.size.width}px) --`);
    await page.setViewportSize(vp.size);

    // Dashboard. Its nav is a three-item row at both widths, but the shell CSS is
    // shared, so it is worth a look in each.
    await page.goto(BASE + "/", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(500); // let the SSE feed paint a few rows
    await analyze(page, `${vp.key} dashboard`);

    // Messages. Scanned in both its states: the compose form as it loads, and
    // again with an over-length body, because the budget and the error note are
    // the two things on the page that change colour to mean something.
    await page.goto(BASE + "/messages.html", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(400);
    await analyze(page, `${vp.key} messages`);
    await page.evaluate(() => {
      const box = document.querySelector("#msg-text");
      if (!box) return;
      box.value = "A".repeat(200);
      box.dispatchEvent(new Event("input"));
    }).catch(() => {});
    await page.waitForTimeout(150);
    await analyze(page, `${vp.key} messages (over length)`);

    // Every rendered panel: each top-level tab, and each Modes sub-tab.
    for (const t of TARGETS) {
      await open(page, t.tab, t.sub);
      await analyze(page, `${vp.key} settings#${t.sub ? `${t.tab}/${t.sub}` : t.tab}`);
      // The General tab has a second state worth scanning: the DMR ID lookup with
      // an answer in it. Done after the plain pass so the panel above is scanned
      // as it actually loads.
      if (t.tab === "general" && !t.sub) {
        await openIDLookup(page);
        await analyze(page, `${vp.key} settings#general (ID lookup answered)`);
      }
      // The Phonebook panel's two other states: the grant form, and an account
      // whose controls are disabled because it is the last admin. Both are after
      // the plain pass, so the panel above is still scanned as it loads.
      if (t.tab === "phonebook") {
        await openPhonebookStates(page, analyze, vp.key);
      }
    }

    // The forced-rotation screen, in its own context — see the note on the
    // function. Placed here so it is walked at both viewports like everything
    // else on this page.
    await scanRotationScreen(browser, theme, mode, vp, analyze);

    if (vp.key === "desktop") {
      // Both states of every sidebar group. Expanded is the default the walk above
      // already ran under; collapsing them all covers the other state for each, then
      // the nav is put back so the state does not leak into the next viewport.
      await setGroups(page, false);
      await analyze(page, `${vp.key} nav (groups collapsed)`);
      await setGroups(page, true);
      await analyze(page, `${vp.key} nav (groups expanded)`);
    } else {
      // The tile grid, which replaces the panel on a narrow viewport.
      await page.evaluate(() => window.showNavGrid && window.showNavGrid(false)).catch(() => {});
      await page.waitForTimeout(150);
      await analyze(page, `${vp.key} nav (tile grid)`);
    }
  }

  await context.close();
}

await browser.close();
console.log(`\n${scans} page(s) scanned across ${THEMES.length} theme(s) × ${MODES.length} mode(s); ${violations} violation(s).`);
if (violations) {
  console.error("\nAccessibility gate FAILED — fix the violations above (see helpUrl for guidance).");
  process.exit(1);
}
console.log("Accessibility gate passed.");
