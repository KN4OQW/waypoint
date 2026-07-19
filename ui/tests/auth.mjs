// Browser-driven functional tests for the Waypoint claim/login UI (RFC-0002).
//
// The a11y gate already proves the claim and login screens render and let an
// operator into the app; this covers the branches it does not exercise — client
// validation, the 409 "someone else claimed" race surface, the generic bad-login
// message, and the central 401 handling that sends an expired session to the login
// screen and back to where it was.
//
// It follows the a11y harness's convention (a plain-node Playwright script, no test
// runner) but manages its own daemons: claim is one-way, so each scenario spawns a
// fresh `waypointd -demo` over a throwaway temp store to control the claim state.
//
// Env:
//   WAYPOINTD  path to a built waypointd binary (required)

import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const WAYPOINTD = process.env.WAYPOINTD;
if (!WAYPOINTD) {
  console.error("WAYPOINTD must point to a built waypointd binary");
  process.exit(2);
}
const CREDS = { username: "kn4oqw", password: "goodpassword" };

// --- daemon lifecycle ----------------------------------------------------
function freePort() {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

async function waitHealth(base, tries = 60) {
  for (let i = 0; i < tries; i++) {
    try {
      const r = await fetch(base + "/api/health");
      if (r.ok) return await r.json();
    } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("daemon did not become healthy: " + base);
}

// startDaemon brings up a fresh demo daemon over its own temp store and returns a
// handle with the base URL and a stop() that kills the process and removes the dir.
async function startDaemon() {
  const dir = mkdtempSync(join(tmpdir(), "wp-uitest-"));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const proc = spawn(WAYPOINTD, ["-demo", "-addr", `127.0.0.1:${port}`, "-store", join(dir, "config.db")], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  try {
    await waitHealth(base);
  } catch (e) {
    proc.kill("SIGKILL");
    rmSync(dir, { recursive: true, force: true });
    throw new Error(e.message + "\n" + log);
  }
  return {
    base,
    stop() {
      proc.kill("SIGKILL");
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

// claimViaApi claims a daemon out-of-band (used to reach the "claimed" state, and to
// force the 409 race under the claim form).
async function claimViaApi(base) {
  const r = await fetch(base + "/api/claim", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(CREDS),
  });
  if (r.status !== 201) throw new Error("setup claim failed: " + r.status);
}

// --- assertions / runner -------------------------------------------------
let failures = 0;
let ran = 0;
function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}
async function test(name, fn) {
  ran++;
  const d = await startDaemon();
  const context = await browser.newContext({ baseURL: d.base });
  const page = await context.newPage();
  try {
    await fn(page, d.base, context);
    console.log(`  ok   ${name}`);
  } catch (e) {
    failures++;
    console.log(`  FAIL ${name}`);
    console.log(`       ${(e && e.message ? e.message : String(e)).split("\n").join("\n       ")}`);
  } finally {
    await context.close();
    d.stop();
  }
}

async function gotoAuth(page, base) {
  await page.goto(base + "/", { waitUntil: "domcontentloaded" });
  await page.waitForSelector("body[data-mode]", { timeout: 10000 });
  return page.evaluate(() => document.body.dataset.mode);
}

// --- the browser ---------------------------------------------------------
const browser = await chromium.launch();

// Claim happy path: a fresh device shows the claim screen; a valid claim lands the
// operator straight in the app (no second login) with a session cookie.
await test("claim → app (no second login)", async (page, base, context) => {
  const mode = await gotoAuth(page, base);
  assert(mode === "claim", `fresh device mode = ${mode}, want claim`);
  await page.fill("#claim-username", CREDS.username);
  await page.fill("#claim-password", CREDS.password);
  await page.fill("#claim-confirm", CREDS.password);
  await page.click("#claim-submit");
  await page.waitForSelector(".app", { timeout: 10000 });
  assert(new URL(page.url()).pathname === "/", `landed on ${page.url()}, want /`);
  const cookies = await context.cookies();
  assert(cookies.some((c) => c.name === "waypoint_session" && c.value), "no session cookie after claim");
});

// Client-side validation mirrors the API floor: a confirm mismatch and a short
// password are caught before any request, so the device stays unclaimed.
await test("claim validation blocks mismatch + short password", async (page, base) => {
  await gotoAuth(page, base);

  // Mismatched confirm.
  await page.fill("#claim-username", CREDS.username);
  await page.fill("#claim-password", "longenough1");
  await page.fill("#claim-confirm", "different123");
  await page.click("#claim-submit");
  await page.waitForSelector("#claim-confirm-err:not(:empty)", { timeout: 5000 });
  assert(await page.isVisible("#claim-form"), "left the claim form on a mismatch");

  // Short password.
  await page.fill("#claim-confirm", "short");
  await page.fill("#claim-password", "short");
  await page.click("#claim-submit");
  await page.waitForSelector("#claim-password-err:not(:empty)", { timeout: 5000 });

  // Nothing was submitted: the device is still unclaimed.
  const h = await (await fetch(base + "/api/health")).json();
  assert(h.claimed === false, "a rejected claim still reached the server");
});

// Losing the claim race: the device is claimed from elsewhere while the claim form
// is open. Submitting gets a 409, surfaced clearly, and the screen switches to
// login so the operator can proceed with the winner's credentials.
await test("claim 409 surfaces + switches to login", async (page, base) => {
  const mode = await gotoAuth(page, base);
  assert(mode === "claim", "expected the claim screen");
  await claimViaApi(base); // someone else wins the race

  await page.fill("#claim-username", "late");
  await page.fill("#claim-password", "anotherpass1");
  await page.fill("#claim-confirm", "anotherpass1");
  await page.click("#claim-submit");

  await page.waitForSelector("#login-form", { state: "visible", timeout: 10000 });
  const notice = (await page.textContent("#notice")) || "";
  assert(/claimed/i.test(notice), `409 notice = ${JSON.stringify(notice)}`);
  assert(!(await page.isVisible("#claim-form")), "claim form still visible after 409");
});

// A claimed device shows login; wrong credentials get a single generic message —
// never a username/password distinction (mirrors the server's one failLogin reply).
await test("login rejects bad credentials generically", async (page, base) => {
  await claimViaApi(base);
  const mode = await gotoAuth(page, base);
  assert(mode === "login", `claimed device mode = ${mode}, want login`);
  await page.fill("#login-username", CREDS.username);
  await page.fill("#login-password", "wrongwrong");
  await page.click("#login-submit");
  await page.waitForSelector("#notice:not([hidden])", { timeout: 10000 });
  const notice = (await page.textContent("#notice")) || "";
  assert(/invalid username or password/i.test(notice), `login notice = ${JSON.stringify(notice)}`);
  assert(await page.isVisible("#login-form"), "left the login form on a bad login");
});

// Central 401 handling: an expired/revoked session on any gated call routes to the
// login screen preserving where the operator was, and logging in returns them there.
await test("expired session → login → back to where you were", async (page, base, context) => {
  await claimViaApi(base);

  // Log in through the UI, then move to a specific settings tab.
  await gotoAuth(page, base);
  await page.fill("#login-username", CREDS.username);
  await page.fill("#login-password", CREDS.password);
  await page.click("#login-submit");
  await page.waitForSelector(".app", { timeout: 10000 });
  await page.goto(base + "/settings.html#dmr", { waitUntil: "domcontentloaded" });
  await page.waitForSelector(".app", { timeout: 10000 });

  // Session dies (logout elsewhere / idle expiry). The next gated call is a 401.
  await context.clearCookies();
  // The wrapped fetch routes that 401 to the login page. That redirect can tear
  // down this evaluate's execution context before the call resolves — a benign
  // navigation race ("Execution context was destroyed"). The real assertion is the
  // waitForURL below, so swallow only that rejection rather than letting it flake.
  await page.evaluate(() => { fetch("/api/config"); }).catch(() => {});

  await page.waitForURL(/\/\?next=/, { timeout: 10000 });
  const next = new URL(page.url()).searchParams.get("next");
  assert(next && next.startsWith("/settings.html"), `next = ${JSON.stringify(next)}`);
  await page.waitForSelector("#login-form", { state: "visible", timeout: 10000 });

  // Log back in: the auth page honours ?next and returns us to the settings tab.
  await page.fill("#login-username", CREDS.username);
  await page.fill("#login-password", CREDS.password);
  await page.click("#login-submit");
  await page.waitForURL(/\/settings\.html/, { timeout: 10000 });
  await page.waitForSelector(".app", { timeout: 10000 });
  assert(new URL(page.url()).pathname === "/settings.html", `returned to ${page.url()}`);
});

await browser.close();
console.log(`\n${ran} scenario(s); ${failures} failure(s).`);
if (failures) {
  console.error("\nUI functional tests FAILED.");
  process.exit(1);
}
console.log("UI functional tests passed.");
