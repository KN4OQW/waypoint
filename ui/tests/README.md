# UI functional tests (claim / login)

Browser-driven tests for the first-boot claim and login UI (RFC-0002, issue #10).
The [accessibility gate](../a11y/) proves the claim and login screens render and
let an operator into the app; this harness covers the behaviour it does not — the
branches around getting auth *wrong* and sessions *expiring*:

- **Claim → app** — a valid claim lands straight in the app, no second login.
- **Claim validation** — a confirm mismatch and a sub-8-character password are
  caught client-side (mirroring the API floor) before any request is sent.
- **Claim 409/401** — when the device is claimed from elsewhere first, the claim
  form surfaces it and switches to the login screen.
- **Login rejects bad credentials generically** — one message, never a
  username-vs-password distinction.
- **Expired session → login → back** — a 401 on any gated call routes to the login
  screen preserving `?next`, and logging in returns the operator to where they were.

Claim is one-way, so — unlike the a11y harness, which drives one long-lived daemon —
this spawns a fresh `waypointd -demo` over a throwaway temp store per scenario to
control the claim state. CI runs it on every pull request
(`.github/workflows/ui-tests.yml`).

## Run it locally

From the repo root:

```sh
go build -o waypointd ./cmd/waypointd

cd ui/tests
npm ci
npx playwright install chromium   # first run only
WAYPOINTD="$PWD/../../waypointd" npm test
```

A clean run prints `UI functional tests passed.` and exits 0; any failed scenario
prints its assertion and the process exits non-zero.

### Env knobs

| Variable | Purpose |
| --- | --- |
| `WAYPOINTD` | Path to a built `waypointd` binary (required). Each scenario launches it with `-demo` over its own temp store on a free port. |
