# RW-D1/D2/D3 runtime readiness record

25 September 2026 · Lane D worker (RW-D1+RW-D2+RW-D3) · Model: Sol-Medium (per coordinator)

## Evidence class (read this first)

This record is **code/config inspection plus the repository's own disposable-store
Go tests run on this machine**. It is not live proof: no app host is selected,
nothing is installed on any host, and no probe below has been executed against
a live environment. Per the plan, fixtures cannot satisfy RW-D1/D2/D3, so every
live status here is `absent` (proven by repo/decision record) or
`unverifiable-here` (provable only on the selected app host at runtime, without
spend, via the listed probe). No status in this file is a readiness claim.
Zero paid calls made, nothing sent, no servers started, no secret or private
values printed (names only).

Sources: `apps/api/cmd/server/main.go`, `internal/{researchwire,researchexecute,
codexservice,jev,jevservice,materialprep,applicationpacks,delivery,
deliveryservice,httpapi}/*`, `ops/i12/{api.env.template,README.md,accept-*.sh,
executor-sandbox.sh}`, prior handoff `/tmp/rw-d0-handoff.md`.

## RW-D1 · Isolated live correction/research/check runtime — verdict: blocked, code verified

### Code-verified fail-closed behavior (no host needed)

- `wireResearch` (`cmd/server/main.go:219`): without Jev, research stays
  unwired with log `research unavailable: TYPESAFE_API_KEY is not set` and all
  research routes 503 (`httpapi/research.go`). Wiring failure logs
  `research unavailable: <err>`; success logs `research wired: artifacts=…
  agent=…`. Absolute artifact root enforced; Jev provider required.
- Executor (`researchexecute/`): empty Chrome/Python paths fail those kinds
  closed (`backend_not_configured`); digest mismatch fails closed
  (`backend_untrusted`); missing/unusable sandbox binary fails browser/exec
  kinds closed (`sandbox_unavailable`). Scratch root created `0700` at
  construction. Production dispatch refuses non-public addresses
  (`PermitLoopback` never set in `main.go`).
- Codex runner (`codexservice/connection.go`, `readiness.go`): SSH mode
  requires host/user/launcher/identity/known-hosts, `…_ISOLATION_VERIFIED=true`,
  and a ≥32-char bridge token; otherwise `Status`/`CheckRound`/`DialOneShot`
  fail closed (`runner_not_configured`, `isolation_not_verified`,
  `bridge_not_configured`, `runner_configuration_invalid`, `model_not_configured`,
  `needs_sign_in`, …). `CheckRound` commissions nothing. Local-runner mode is
  explicit only and claims no isolation (`status.local`).
- Answer matching (`httpapi/answermatch.go:260`): candidate batches with no
  matcher 503 (`jev_unavailable`, partial batches stay saved); deterministic
  no-candidate batches resolve locally without Jev. Correction/check service
  behavior is lane A's; its runtime needs are exactly runner + Jev + research
  backends above.

### Proved wiring gap: `JOBSEEK_RESEARCH_SANDBOX_BINARY` is dead configuration

`grep` over `apps/api --include=*.go` shows the variable is read in **zero**
production files, and `researchwire.Config` (`researchwire/wire.go:41`) has **no**
`SandboxBinary` field, so `Wire()` builds `researchexecute.Config` without it
and `withDefaults()` always substitutes macOS `/usr/bin/sandbox-exec`
(`executor.go:83`, `sandbox.go:27`). On a Linux app host, browser/exec research
kinds therefore fail closed (`sandbox_unavailable`) even with a correct install
carrying the template's `/usr/local/libexec/jobseek-executor-sandbox`. The fix
spans two files (`researchwire/wire.go` field + pass-through, `main.go` env
read); the second half alone would not compile, so no production edit lands in
this assignment — full diff text is in `/tmp/rw-d123-handoff.md` for coordinator
dispatch. Related install-time check: the T02 hard-coded Chrome pin
(`backends.go:23`, `a0bfe7b4…`) differs from the T22/Linux template pin
(`ded93a9c…`); env takes precedence, so the deployer must install exactly the
env-pinned build (re-verified with `--version` + digest at first use).

### Named blockers (D1)

| # | Blocker | Owner |
|---|---------|-------|
| D1-1 | Host selection: `infra` vs `linux` + separate runner VM | owner decision; coordinator records |
| D1-2 | App-host install (`ops/i12/install-app.sh`, `api.env`, TLS, firewall) | operator |
| D1-3 | Runner VM install + containment proofs (`accept-boundary.sh`, transport/cgroup/supervisor-death, native-profile live turn, `accept-idle.sh`); ChatGPT device sign-in; only then `…_ISOLATION_VERIFIED=true` | operator + owner |
| D1-4 | `TYPESAFE_API_KEY` installed in root-owned `0600` `api.env` | operator installs; owner supplies value |
| D1-5 | Actually-available `JOBSEEK_CODEX_MODEL` / `JOBSEEK_CODEX_EFFORT` values | owner selects |
| D1-6 | Retrieval backends: pinned headless-shell, python3, executor wrapper, artifact/scratch roots; sandbox-binary wiring fix deployed | operator installs; coordinator dispatches fix |
| D1-7 | Same intended instance serves the UI/API paths under test | operator (install topology) |

## RW-D2 · Material-rendering runtime readiness — verdict: blocked, code verified

### Code-verified fail-closed behavior (no host needed)

- `options.Materials` is constructed only when Jev is enabled **and** both
  `JOBSEEK_APPROVED_CAREER_ROOT` and `JOBSEEK_TYPST_PATH` are set
  (`main.go:101`); otherwise prepare/edit/rewrite all 503
  (`httpapi/materials.go`, `materialprep.ErrUnavailable`).
- Drafter is nil unless both `JOBSEEK_CODEX_MODEL` and `JOBSEEK_CODEX_EFFORT`
  are set (`main.go:109`); required-answer drafting and explicit rewrite then
  503 instead of inventing drafts. `CodexDrafter` runs one bounded one-shot
  turn (`codexservice.OneShot`: approvalPolicy=never, no tools, prompt/output/
  message bounds) over verified facts only; output is scope- and
  citation-validated before use (`materialprep/draft.go`).
- Rewrite is explicit-only: `RewriteOpportunityMaterials` is the sole caller of
  the rewrite turn and the store rewrite, reusing the drafter's runner, so
  rewrite can never run without the drafting path's bounded primitive
  (`materialprep/rewrite.go`). Direct exact edits save bytes with no model call.
- Career loader (`applicationpacks/sources.go`) accepts only registry files
  with exact sha256 digests (template `cv-vince-liem.typ` mandatory); any
  change fails closed. Note: the registry holds 4 files but `main.go:116`
  wires 3 (`cv-vince-liem.typ`, `cv-vince-liem.md`,
  `github-evidence-review.md`); `portfolio-case-studies.md` is allowed by the
  loader but not wired — the install must contain at least the 3 wired pinned
  files.
- Renderer (`applicationpacks/prepare.go:243`) requires a Typst path, absolute
  private temp dir (`0700`), and timeout ≤30s; compiles via
  `typst compile --root <opdir>` in a fresh per-pack temp dir. Research
  readiness implies nothing about materials: the two wirings gate independently.

### Named blockers (D2)

| # | Blocker | Owner |
|---|---------|-------|
| D2-1 | Runner + model/effort (same as D1-3/D1-5): one-shot drafting dials the isolated runner | owner + operator |
| D2-2 | Approved career root installed at the configured path with exact pinned digests | operator installs; owner owns asset truth |
| D2-3 | Typst 0.15.1 installed at `JOBSEEK_TYPST_PATH` (`/opt/jobseek/bin/typst`) | operator |

## RW-D3 · Isolated safe test destination — verdict: blocked, code verified

### Code-verified fail-closed behavior (no host needed)

- `options.Delivery.Sender` exists only when all six `JOBSEEK_SMTP_*` vars
  (`ADDRESS/FROM/SERVER_NAME/HELLO_NAME/USERNAME/PASSWORD`) are set and validate
  (`main.go:136`, `delivery/smtp.go:51`); otherwise `delivery/capability`
  reports `submissionAvailable:false` and send refuses with `ErrUnavailable`
  (`deliveryservice/service.go:195`, `httpapi/delivery.go:13`).
- Sends are digest-bound to owner-reviewed material: approval sha must equal the
  material sha, `VerifyReviewMaterials` re-checks immediately before send, and
  the SMTP layer re-verifies the approved MIME digest before connecting
  (`delivery/smtp.go:74`). SMTP acceptance (250 after DATA) is reported as
  submission only; uncertain outcomes are never auto-retried.
- No receipt lookup exists by design: `receiptLookup` is always false,
  resume/reconcile of a delivery round always report unsupported, and closing an
  unresolved delivery records `employerReceiptVerified:false`.

### Honest isolation limit (no code change made; procedure must carry it)

The recipient is the **evidenced route destination** (`selected.DestinationText`,
`deliveryservice/service.go:160`), digest-bound against later change — but
nothing in code distinguishes a test sink from an employer address. Test
isolation is therefore operational, not code-enforced: the operator must
provision an SMTP account that can deliver **only** to the controlled mailbox,
the checked role's route must point at the controlled destination, and the
owner must inspect the exact destination/channel/material/version at review
before the explicit send in RW-G4. No send occurred in this task.

### Named blockers (D3)

| # | Blocker | Owner |
|---|---------|-------|
| D3-1 | Isolated test SMTP sink provisioned (account + controlled mailbox; no employer path) | operator provisions; owner confirms destination identity |
| D3-2 | Six `JOBSEEK_SMTP_*` vars installed for the sink only | operator installs; owner supplies account values |
| D3-3 | Controlled-destination route + exact-review procedure confirmed for RW-G4 | owner + coordinator |

## No-spend probes for the future app host

Run on the selected host after install. None commissions work, spends, or sends.
Presence/absence only — never print secret values or private documents.

- `GET /api/v1/health` — liveness only (no auth).
- `GET /api/v1/codex/status` (owner session) — expect progression
  `runner_not_configured` → `isolation_not_verified` → `needs_sign_in` →
  `owner_sign_in_pending` → `ready` as install/accept/sign-in complete; `local`
  must be false, `model`/`effort` must show the owner-selected values.
- `GET /api/v1/delivery/capability` (owner session) — `submissionAvailable`
  true only with all six SMTP vars; `receiptLookup` always false.
- Research routes (e.g. `POST /api/v1/research/runs`) — 503 `unavailable`
  while unwired is the honest no-spend signal; wired stacks accept supervision.
- Materials routes (`…/materials/prepare`, `PUT …/materials/current`,
  `…/materials/rewrite`) — 503 while `options.Materials`/drafter missing.
- `POST …/answers/match` with candidates — 503 `Answer matching needs Jev`
  while the matcher is nil.
- Startup log lines: `research unavailable: …` vs
  `research wired: artifacts=<root> agent=<id>`; `jobseek API listening on …`.
- File/env presence (operator reads privately): pinned headless-shell path +
  `--version` + digest equals `JOBSEEK_RESEARCH_CHROME_SHA256`; python3
  `--version`; wrapper executable at `JOBSEEK_RESEARCH_SANDBOX_BINARY`;
  artifact/scratch roots `0700` and scratch empty while idle; career root holds
  the 3 wired pinned files (digests match the loader registry); Typst binary
  runs (`<typst> --version`); `api.env` root-owned `0600`.
- Confinement proofs before any research run:
  `accept-executor-confinement.sh /usr/local/libexec/jobseek-executor-sandbox
  /opt/jobseek/headless-shell/chrome-headless-shell` (app host) and
  `accept-boundary.sh` + runner probes + native-profile live turn
  (runner VM). Any failure leaves the capability unavailable, never unmediated.
- Known observability gap: no single endpoint summarizes Jev/research/
  materials/runner presence; assemble status from `codex/status` +
  `delivery/capability` + per-feature 503s + startup logs.

## Verification performed here

- `go vet` on `researchexecute researchwire materialprep applicationpacks
  delivery deliveryservice`: clean.
- `go test -count=1` on the above plus `jev`: all `ok` (disposable stores).
- `go test -count=1 ./internal/codexservice/`: `ok`.
- `gofmt -l` on `cmd/server internal/researchwire internal/researchexecute`:
  clean.
- Changed files in this assignment: none (new files only: this record;
  handoff at `/tmp/rw-d123-handoff.md`). No `main.go` edit: the proved
  sandbox-binary fix requires the unwritable `researchwire/wire.go` half to
  compile (see handoff diff).
