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
  "profiles", "updates", "system", "expert",
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

// Open a settings target. The canonical deep link for a mode panel is
// "#modes/<sub>"; selectTab is called as well because a hash-only goto is a
// same-document navigation and does not re-run the page script.
async function open(page, tab, sub) {
  const hash = sub ? `${tab}/${sub}` : tab;
  await page.goto(`${BASE}/settings.html#${hash}`, { waitUntil: "domcontentloaded" });
  await page.evaluate(([t, s]) => window.selectTab && window.selectTab(t, s), [tab, sub || ""]).catch(() => {});
  await page.waitForTimeout(250);
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
  const page = await context.newPage();

  for (const vp of VIEWPORTS) {
    console.log(`  -- viewport: ${vp.key} (${vp.size.width}px) --`);
    await page.setViewportSize(vp.size);

    // Dashboard. Its nav is a two-item row at both widths, but the shell CSS is
    // shared, so it is worth a look in each.
    await page.goto(BASE + "/", { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(500); // let the SSE feed paint a few rows
    await analyze(page, `${vp.key} dashboard`);

    // Every rendered panel: each top-level tab, and each Modes sub-tab.
    for (const t of TARGETS) {
      await open(page, t.tab, t.sub);
      await analyze(page, `${vp.key} settings#${t.sub ? `${t.tab}/${t.sub}` : t.tab}`);
    }

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
