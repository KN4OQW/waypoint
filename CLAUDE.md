# Working on Waypoint

Instructions for Claude and any other AI assistant working in this repository,
whether through the `@claude` workflows in `.github/workflows/` or a local agent.
[CONTRIBUTING.md](CONTRIBUTING.md) binds humans; this binds you, and where the two
disagree, CONTRIBUTING wins.

Read this before writing code. Most of it is not inferable from the source.

## Commands

```sh
go build ./...          # daemon
go test ./...           # must pass; this is the floor, not the bar
go vet ./...
go generate ./...       # CI fails if any generated file is stale
golangci-lint run --new-from-rev=origin/main   # the lint gate is differential
```

The lint baseline is deliberate: findings in code a branch touched fail CI,
findings in code it did not are reported and do not. New code stays at zero. Do
not "fix the baseline" as a side quest inside a feature branch — a sweep through
a dozen unrelated packages is a review nobody can do properly.

Never hand-edit a generated file. `ui/static/locales/index.json` is generated from
each catalog's `_meta` block; adding a language is a new catalog plus
`go generate ./...`.

## Tests are the deliverable, not the receipt

**Every behaviour change ships with tests.** "I ran `go test ./...`" is not
compliance — that runs somebody else's tests. The question is what your change
asserts that nothing asserted before.

The project tests in two tiers, and they make different claims:

- **Tier 1** (`internal/**/*_test.go`) — ordinary unit tests. They prove the code
  does what it meant to: that a renderer emits the bytes it intended, that a
  validator refuses what it meant to refuse.
- **Tier 2** (`test/tier2/`, build tag `tier2`) — the real, pinned upstream
  daemons driven against configs our renderers produced, over the mode loopbacks,
  with no RF and no upstream credential. They prove the daemon *agrees* those
  bytes are a valid configuration, and that traffic lands where the generated
  routing says. That is a different claim, and Tier 1 cannot make it.

If you change a renderer, ask whether Tier 2 can check the daemon still accepts
the result. If you add a rule asserting some configuration is broken, ask what
evidence you have — see below.

### Rules for a check that says something is wrong

This repository is unusually strict here, for a reason: a false finding tells an
operator their working node is broken, and trains them to ignore the panel.

1. **Cite the evidence at the check.** The upstream source line, or the protocol
   field width. `mode_readiness.go` and `gateway_requirements.go` are the worked
   examples — every rule names what it rests on.
2. **A rule you cannot ground gets left out.** Not softened to a warning — left
   out. `mode_readiness.go` deliberately has no NXDN ID requirement, with a test
   asserting that absence so nobody adds one later on symmetry.
3. **A check that fires on a good configuration is worse than no check.** Every
   table of failure cases carries its clean cases beside it.
4. **Reading upstream's source is not evidence that a daemon fails.** Upstream
   changes. Where a claim is checkable against the real binary, check it — in
   both directions, because a check that fires on everything proves nothing.
5. **Refusing and reporting are different powers.** Refusing a save, withholding
   a daemon, and reporting a problem are three separate mechanisms with three
   different bars. Do not promote a finding to a refusal without deciding to.

### Correctness properties that are load-bearing

- **Config round-trip losslessness.** A store → render → parse → store cycle must
  not lose a field. Tests enforce it. A PR that breaks it is caught by CI, not by
  a user losing their DMR password.
- **Secrets are write-only in projections.** The View carries `HasPassword`, never
  the value. Reporting that a secret is unset must never disclose one — carry
  field *names*, never values.
- **Renders are pure.** A renderer reads the model and returns a string. Ordering
  is stable so a caller diffing two runs sees a changed configuration, not a
  changed map iteration.

## Comments explain why

The comment density here is far above normal and it is deliberate. Match it.

A comment that restates the code is noise; a comment that records *why this and
not the obvious alternative* is the thing that survives. When you discover
something the hard way — an upstream assert, a parser that treats an empty key as
zero rather than unset, a daemon that does not reconnect — write it down where the
next person will hit it. Say what you measured, not what you assumed.

Where you correct an earlier claim in the codebase, say that it was wrong and what
misled it. See the survey comment in `gateway_requirements.go`.

## Copy and UI

- Users manage **networks** and **modes**, not "config sections".
- Errors say what went wrong *and what to do*.
- **Accessibility is a merge gate.** Status is never conveyed by colour alone;
  interactive elements need keyboard, focus and ARIA coverage; contrast meets
  WCAG AA in every theme. The `a11y` CI job enforces it.
- UI strings live in flat JSON catalogs under `ui/static/locales/`, `en-US.json`
  being the base. A user-facing string generated in Go cannot be translated —
  if you find yourself wanting one, that is a design decision to raise, not to
  make quietly.

## Privacy is a merge gate

A Waypoint device contacts project infrastructure only to check for updates and
refresh public host/ID databases, both user-disableable. Adding *any* new outbound
request needs the PR template's privacy section filled in: no device identifier,
gated on an operator-visible off switch, documented, and covered by a test rather
than by intent. When in doubt, the answer is no.

## Process

- **DCO sign-off on every commit** (`git commit -s`). No CLA.
- Commit messages: imperative summary line; the body explains *why*. Look at
  `git log` before writing one — the house style is prose, not bullet points.
- Branch and open a PR; never commit to `main`.
- **RFCs are dormant pre-v1.** See [GOVERNANCE.md](GOVERNANCE.md#rfcs). Do not
  tell a contributor to open an RFC, and do not open one. Architecture decisions
  are recorded in `docs/design/` and in the code comment that implements them.
  Existing numbered RFCs remain the design record and code cites them freely.

## Reporting your own work

Say what you did not do as plainly as what you did. If a test was written but
never executed, say so and say why. If part of the scope was skipped, name it.
A summary that reads as complete when it is not costs more than the work saved —
the reviewer's whole job is knowing what to trust.
