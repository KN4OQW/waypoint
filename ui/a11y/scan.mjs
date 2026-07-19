// Automated accessibility gate for the Waypoint UI.
//
// Loads the dashboard and every settings/wizard tab in a real headless Chromium
// and runs axe-core against each, in all three display themes. Any WCAG 2.1
// A/AA violation fails the process (non-zero exit) so CI can gate merges on it.
//
// It drives the *running daemon* (waypointd -demo), not the raw static files, so
// the panels render with live data exactly as an operator sees them.
//
// Env:
//   BASE                 base URL of a running `waypointd -demo` (default http://127.0.0.1:8073)
//   PLAYWRIGHT_CHROMIUM  explicit Chromium binary (optional; omit to use Playwright's own)
//   A11Y_THEMES          comma list of themes to test (default phosphor,amber,ice)

import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";

const BASE = process.env.BASE || "http://127.0.0.1:8073";
const THEMES = (process.env.A11Y_THEMES || "phosphor,amber,ice").split(",").map((s) => s.trim()).filter(Boolean);
const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];

// The device is gated behind the RFC-0002 claim state machine, so the dashboard and
// settings only render for an authenticated session. The harness drives a fresh
// (unclaimed) demo daemon: the first context claims the device (scanning the claim
// screen on the way in), and every later context logs in (scanning the login
// screen), so the claim and login pages are covered by the same gate that renders
// the app. Credentials are throwaway — the daemon store is discarded after the run.
const CREDS = { username: "a11yadmin", password: "a11y-scan-pass" };

// Every settings/wizard tab (mirrors the TABS list in settings.js).
const TABS = [
  "general", "setup", "lcd", "dmr", "dstar", "ysf", "p25", "nxdn",
  "m17", "pocsag", "fm", "modes", "brandmeister", "gateways", "network", "expert",
];

const launchOpts = {};
if (process.env.PLAYWRIGHT_CHROMIUM) launchOpts.executablePath = process.env.PLAYWRIGHT_CHROMIUM;

const browser = await chromium.launch(launchOpts);
let violations = 0;
let scans = 0;

function report(label, result) {
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

// enterApp scans whichever pre-auth screen the gate is serving (claim on a fresh
// device, login once it is claimed) and then authenticates through it, landing on
// the dashboard with a live session. Claiming is one-way, so across a run the first
// context claims and the rest log in — this handles either without the caller
// caring which. Both screens are held to the same WCAG bar as the app.
async function enterApp(page, theme) {
  await page.goto(BASE + "/", { waitUntil: "domcontentloaded" });
  await page.waitForSelector("body[data-mode]", { timeout: 10000 }); // set once /api/health resolves
  const mode = await page.evaluate(() => document.body.dataset.mode);
  await analyze(page, `auth:${mode} (${theme})`);

  if (mode === "claim") {
    await page.fill("#claim-username", CREDS.username);
    await page.fill("#claim-password", CREDS.password);
    await page.fill("#claim-confirm", CREDS.password);
    await page.click("#claim-submit");
  } else {
    await page.fill("#login-username", CREDS.username);
    await page.fill("#login-password", CREDS.password);
    await page.click("#login-submit");
  }
  // Auth navigates to the app on success; the dashboard shell carries `.app`.
  await page.waitForSelector(".app", { timeout: 10000 });
}

async function analyze(page, label) {
  // Toggle every off-state control on, so we also exercise the "enabled" accent
  // styling (pills, mode tiles) that the default render leaves off.
  await page.evaluate(() => {
    document.querySelectorAll('.mode-card.off, .pill.off, [aria-pressed="false"]').forEach((b) => {
      if (typeof b.click === "function") b.click();
    });
  }).catch(() => {});
  await page.waitForTimeout(150);
  const result = await new AxeBuilder({ page }).withTags(TAGS).analyze();
  report(label, result);
}

for (const theme of THEMES) {
  console.log(`\n=== theme: ${theme} ===`);
  const context = await browser.newContext();
  await context.addInitScript((t) => localStorage.setItem("wp-theme", t), theme);
  const page = await context.newPage();

  // Claim/log in through the pre-auth screen (scans it), landing on the dashboard.
  await enterApp(page, theme);

  // Dashboard.
  await page.goto(BASE + "/", { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(500); // let the SSE feed paint a few rows
  await analyze(page, "dashboard");

  // Every settings/wizard tab.
  for (const tab of TABS) {
    await page.goto(BASE + "/settings.html#" + tab, { waitUntil: "domcontentloaded" });
    await page.evaluate((id) => window.selectTab && window.selectTab(id), tab).catch(() => {});
    await page.waitForTimeout(250);
    await analyze(page, "settings#" + tab);
  }

  await context.close();
}

await browser.close();
console.log(`\n${scans} page(s) scanned across ${THEMES.length} theme(s); ${violations} violation(s).`);
if (violations) {
  console.error("\nAccessibility gate FAILED — fix the violations above (see helpUrl for guidance).");
  process.exit(1);
}
console.log("Accessibility gate passed.");
