# RW-D1/D2/D3 runtime readiness record

25 September 2026 · Lane D worker (RW-D1+RW-D2+RW-D3) · Model: Sol-Medium (per coordinator)

## Evidence class (read this first)

This record is **code/config inspection plus the repository's own disposable-store
Go tests run on this machine**. It is not live proof: the owner selected this MacBook as the app host,
and Muse Code 1.3.0 is installed locally, but the application is not wired to it
and no live-provider probe below has been executed. Per the plan, fixtures cannot satisfy RW-D1/D2/D3, so every
live status here is `absent` (proven by repo/decision record) or
`unverifiable-here` (requiring supported MacBook runtime checks or a separately authorized live exercise). No status in this file is a readiness claim.
Zero paid/model calls were made during this documentation update; nothing sent,
no servers started, and no secret or private values printed (names only).

**Historical provider-direction note superseded (25 September 2026):** The Codex/OpenRouter selector language below reflects an earlier plan and no longer governs first-runtime work. Current owner direction is the locally installed Muse Code CLI on the owner's MacBook for both tiers: Contributor for the entire generalized public vacancy-discovery loop (source/query selection, public search and vacancy capture) and Standard only for grounded application preparation and explicit owner-requested rewrite. Jev does personalized classification/reason choices. The owner reports about 50 practical hours/week on Contributor versus about one on Standard and subscription-backed CLI use, not API-key pay-as-you-go. Preserve that account-specific distinction. Codex/OpenRouter are deferred alternatives; Jev remains separate. No live operation or spend is authorized.

## Current Muse readiness note (25 September 2026)

`muse --version` returned Muse Code 1.3.0 locally. That command emitted a warning that it could not update a timestamp under the user's local binary directory; no installation or login changes were made. Official docs describe `muse serve` with a stable session protocol. Muse SDK session-scoped MCP configuration uses `config.mcpServers` only when the client requests `sessionMcp` during initialize. The reviewed docs do not establish per-invocation MCP support in `muse exec`; do not assume it can invoke recruitment tools. Any application runtime must use the real installed CLI, narrowly scope MCP per session, and never extract credentials or proxy API calls around the harness.

The owner says their installed CLI uses Muse Spark 1.3 Contributor through subscription and selects the same CLI's Standard model only for application preparation and explicit rewrite. The owner reports about 50 hours/week practical use on Contributor versus about one on Standard. Preserve this as direct account evidence; do not equate practical usable time. Confirm the Contributor subscription route through a no-model-call readiness check before discovery; confirm Standard entitlement separately before later Prepare work; checking for accidental API-key override safeguards their stated route and does not challenge it. Published pay-as-you-go API rates are separate: Standard $1.25/M input, $4.25/M output; Contributor $0.10/M input, $0.20/M output. These are not subscription rates or the basis of the owner's reported CLI use. Meta terms §6.2 prohibit personal, sensitive or confidential input to Discounted/Contributor services. Keep owner name, CV, private profile facts and answers out of Contributor prompts and MCP returns. Use Contributor for the entire vacancy discovery loop (choosing sources, general role/region/skill queries, public APIs/company pages/web retrieval, and saving public vacancy evidence); the owner considers those general inputs non-private. Jev handles personalized per-job classification and reason selection, reusing saved assessments. Standard is application-preparation-only. Contributor may author a general reason catalog once per brief version from non-identifying criteria; bind the actual private owner requirements locally/Jev and reuse saved reasons. Known-fact versioning is deterministic. If a new private free-text/CV correction cannot be represented, record a scoped intake gap for owner review; do not call Standard outside Prepare or block public discovery. No model call or live run was made; entitlement, billing route and app/runtime integration remain unverified.

Sources: `apps/api/cmd/server/main.go`, `internal/{researchwire,researchexecute,
codexservice,jev,jevservice,materialprep,applicationpacks,delivery,
deliveryservice,httpapi}/*`, `ops/i12/{api.env.template,README.md,accept-*.sh,
executor-sandbox.sh}`, prior handoff `/tmp/rw-d0-handoff.md`.

## RW-D1 · Live correction/research/check runtime — verdict: blocked, code verified

### Existing Codex-path code only (not Muse readiness)

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
  behavior is lane A's; this legacy runner wiring does not establish the selected Muse path.

### Current sandbox-binary wiring (historical gap resolved in code)

The earlier claim that `JOBSEEK_RESEARCH_SANDBOX_BINARY` was dead configuration is stale. Current `apps/api/cmd/server/main.go` reads that environment variable into `researchwire.Config.SandboxBinary`; `apps/api/internal/researchwire/wire.go` passes it into `researchexecute.Config`. This is code-wiring evidence only. The chosen MacBook still needs local sandbox/retrieval containment, browser pin, source capture and stopping behavior verified before a live run. Do not assume the former Linux-host procedure applies to this MacBook.

### Named blockers and Muse acceptance contract (D1)

| # | Open requirement | Evidence needed |
| --- | --- | --- |
| D1-1 | Public-only Contributor session through the installed local Muse Code 1.3.0 CLI | Pin/verify the 1.3.0 protocol schema, initialize with `sessionMcp`, confirm MCP startup and exact tool inventory, and read effective provider/model plus subscription credential lane before any input; a model list alone does not prove entitlement or billing route. No API-key override, credential extraction or direct API proxy. |
| D1-2 | Separate session/data boundary | Public-only MCP DTO/tool allowlist for source/query choice, general role/region/skill criteria, public vacancy/job detail/actual employer-question evidence and bounded save. Do not expose existing `context_read` owner facts/preferences to Contributor. Keep raw source receipts locally and give Contributor only the public projection. Keep private Jev inputs/results server-side. Never reuse or downgrade a private Standard transcript/checkpoint/workspace into Contributor. |
| D1-3 | Effective tool permissions | `config.mcpServers` is additive: audit inherited host MCP servers, plugins, native tools, hooks, user rules, memory and background observers. Deny or isolate paths that can expose private data or make unobserved calls. MCP tools run outside the shell sandbox, so enforce the public DTO and permissions at the tool boundary. Preserve free choice of public sources and queries. |
| D1-4 | Bounded execution and observation | Bound and capture all model, tool and background usage, including observers, approval judge and subagents, or disable them through documented supported settings. Distinguish accepted command from terminal completion and validated saved results. One first public search targets about 30–45 minutes with a proposed 45-minute stop limit, checkpoints, no automatic repeat and honest no-result coverage. |
| D1-5 | Stop and crash recovery | Stop fences app tools, unqueues pending work, stops background activity and waits for cleanup. On process death reconcile durable cursors and saved receipts before resuming; never blindly replay uncertain requests. Unknown usage remains unknown. |
| D1-6 | Jev and local retrieval | Verify Jev availability and separately billed limits; verify the MacBook's public retrieval containment, source capture, time/byte bounds and scratch/artifact permissions without a live model call. Initial discovery/classification must not trigger deeper per-job research; only owner-selected jobs get explicit checks. |
| D1-7 | Private-context Standard readiness belongs to D2 | Confirm Standard subscription entitlement without a model call before Prepare applications. Standard has zero calls in correction, catalog authoring, discovery, classification, chosen-job checks and Answer. |

Current status: **open**. None of these protocol, isolation, usage, Stop or entitlement requirements has been established by a live Muse-integrated application run. No dollar hard stop is asserted for subscription CLI use; any separately billed Jev/retrieval service needs its own verified cost/limit and owner authorization.

## RW-D2 · Material-rendering runtime readiness — verdict: blocked, code verified

**Current route:** Standard via the installed Muse Code CLI is reserved for Prepare applications: grounded required-answer, tailored CV/message drafting and explicit owner-requested rewrite. Contributor must never receive personal/sensitive/confidential owner data. Confirm Standard entitlement before any live call; no entitlement test via model call.

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
| D2-1 | Muse Standard via the same installed CLI for grounded preparation/rewrite only: confirm subscription entitlement, separate private session/history/workspace, bounded steps/time/usage and no fallback; minimize calls through evidence reuse | operator verifies no-model-call readiness; live preparation remains separately gated |
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

## No-spend readiness procedure on the owner's MacBook

Read-only checks may inspect installed Muse Code 1.3.0 version/protocol metadata, non-secret effective provider/model/auth-lane status, app health and capability GET endpoints, startup logs, configuration *presence*, file permissions, tool inventory and local binary versions/digests. They must not display secret values or private documents. Treat missing entitlement or subscription usage evidence as unknown. A capability GET or a CLI command's acceptance is not proof that a model task completed or that results were saved.

**Do not use production POST/PUT probes as no-spend checks.** `POST /api/v1/research/runs`, materials prepare/rewrite and answers/match can invoke Muse/Jev or mutate data once configured; expecting a 503 is not a safeguard. Test their failure paths only against provider-disabled disposable fixtures. No live mutating probe, provider call, public retrieval, or employer contact occurs before separate authorization. Existing `GET /api/v1/codex/status` describes only the old Codex path and is not Muse readiness proof.

Before live authorization, establish the D1 acceptance contract above through supported local metadata/configuration inspection and controlled provider-disabled fixtures. The first authorized run must confirm terminal completion, saved source receipts, Jev assessments, usage/call trace, Stop behavior and truthful partial/no-result reporting. Subscription CLI usage is bounded by model steps/turns, elapsed time, one run with no automatic repeat and Stop/cancel; it has no meaningful per-run dollar meter here. Separately billed Jev/retrieval services require verified limits and explicit owner authorization.

## Verification performed here

- `go vet` on `researchexecute researchwire materialprep applicationpacks
  delivery deliveryservice`: clean.
- `go test -count=1` on the above plus `jev`: all `ok` (disposable stores).
- `go test -count=1 ./internal/codexservice/`: `ok`.
- `gofmt -l` on `cmd/server internal/researchwire internal/researchexecute`:
  clean.
- This historical verification list predates the current Muse direction and later sandbox-binary wiring. The current documentation update changes no application code and makes no live-readiness claim.

Official references for the direction update: [Muse Code CLI](https://dev.meta.ai/docs/muse-code), [Muse Code automation and sessions](https://dev.meta.ai/docs/muse-code/extending), [Muse SDK session plugins](https://meta-models.github.io/muse-code-sdk/next/guides/plugins/concepts/plugins-in-sdk-sessions/), [Meta model pricing](https://dev.meta.ai/docs/pricing-rate-limits), and [Meta terms §6.2](https://dev.meta.ai/legal/terms-of-service).
