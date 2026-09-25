# E10 · Muse discovery readiness record

25 September 2026 · Lane M · On the owner's MacBook.

## Evidence class (read this first)

This record is **read-only inspection plus the repository's own
provider-disabled Go fixtures run on this machine**. No model call, no Jev
call, no live retrieval, no production POST/PUT probe, no login change and
no server start occurred. Secret and private values were never printed
(names only). A fixture pass never closes a live gate: statuses here are
`observed` (seen by inspection/test), `unknown` (no no-call method
establishes it) or `blocked` (fences the first live run). No status in
this file claims live readiness.

## Verdict

Discovery is **not ready**: the installed CLI reports release `1.4.0`
while the frozen contract pins `1.3.0`, so readiness fails closed with
`muse_version_mismatch` and the session path stays unavailable. The
evidence below is complete for E10; E11 stays closed until the version
pin is re-verified by an explicit M contract change and the owner
authorizes the first run separately.

## Observed facts

- CLI present: `/Users/vince/.local/bin/muse`; `muse --version`
  returns `Muse Code 1.4.0 (1.4.0-R4161.1)`.
- Contract pin: `PinnedCLIVersion = "1.3.0"`
  (`apps/api/internal/musecode/musecode.go:10`).
  `versionMatchesPin("1.4.0")` is false, so `Check` returns
  `muse_version_mismatch` (`readiness.go:45`) and no session input is
  admitted. `JOBSEEK_MUSE_BIN` is unset, so wiring resolves the bare
  `muse` name via PATH (`cmd/server/main.go:256`).
- MSP wire schema exported offline from the installed binary
  (`muse schema generate-json-schema`, stable surface, manifest
  fingerprint `sha256:36466f63…0d3d7f`). `sessionMcp` is a grantable
  client capability and `mcpServers` is present. This schema describes
  the 1.4.0 binary, not the pinned 1.3.0 contract: it is inventory
  evidence, not protocol verification.
- `muse serve` hosts MSP sessions over stdio; sandbox posture is fixed
  at host construction and approval mode is selected on the wire.
- Skill inventory (`muse skills list --source all`): 34 skills — 20
  bundled, 13 user, 1 plugin-scoped. Installed plugins
  (`muse plugins list`): none. The plugin-scoped skill with zero
  installed plugins is an open audit question for the E11 session
  scoping, not a compromise finding.
- Enterprise configuration (`muse config status`): every plane
  (`defaults`/`policy`, system file and macOS managed preferences)
  reports `absent`; no managed override applies.
- `~/.config/muse/settings.json` (non-sensitive values):
  `provider=meta`, `model=muse-spark-1.3-contributor`,
  `reasoning_effort=max`. The file carries no key/token/secret field.
  This is configuration evidence for the Contributor lane, not
  entitlement or billing-route proof.
- `~/.config/muse/auth.json` exists (294 bytes, mode `0600`): login
  material is present. Its values were never read.
- No API-key override observed: `META_API_KEY`, `ANTHROPIC_API_KEY`
  and `OPENAI_API_KEY` are unset in the inspection shell, settings
  carry no credential fields, and the app passes no credentials — the
  session transport is `muse.UnavailableTransport` until the
  E11-authorized live transport exists (`probe.go:55`,
  `cmd/server/main.go` wiring).
- Frozen Contributor tool allowlist, exactly five names
  (`musecode/dto.go:39`): `public_search`, `public_fetch`,
  `public_save_vacancy`, `public_save_question`, `public_list_saved`.
  The legacy private `context_read` tool is never listed, and
  `TestServedInventoryEqualsFrozenAllowlist` fails the suite if the
  served inventory drifts from the frozen set.
- DTO projection (`musecode/dto.go`): `PublicCriteria`,
  `PublicVacancy` and `PublicQuestion` carry general role/region/skill
  criteria and public vacancy/question fields only; saved private
  requirements, raw receipts and Jev data stay server-side.
- Bounds (`musecode/bounds.go:22`): 45-minute wall clock, 120 model
  steps, 400 tool calls, 2 MiB per operation, 200 MiB total,
  background work off. Zero bounds never validate.
- Stop controls: `Supervisor.Stop` fences app tools, unqueues work
  and waits for cleanup (`musecode/supervisor.go:189`);
  `musewire.Service.Stop` (`musewire/service.go:123`); fixtures
  `TestStopFencesLateTools` and `TestStopKeepsPartialSavesHonest`.
- Retrieval containment: `JOBSEEK_RESEARCH_CHROME_PATH`,
  `JOBSEEK_RESEARCH_PYTHON_PATH`, `JOBSEEK_RESEARCH_SANDBOX_BINARY`,
  `JOBSEEK_RESEARCH_SCRATCH_ROOT`, `JOBSEEK_ARTIFACT_ROOT` and
  `JOBSEEK_DATA_DIR` are all unset, so browser/exec retrieval kinds
  fail closed (`backend_not_configured` / `sandbox_unavailable`) and
  `PermitLoopback` is never set in production wiring.
- Provider-disabled fixtures, `go test -count=1`, all `ok`:
  `musecode`, `musewire`, `publicresearch`, `jevservice`,
  `researchcontract`. The full `./apps/api/...` suite also passed
  uncached during E08 verification.

## Unknown facts

- Effective Contributor subscription entitlement and billing route:
  config and login-material presence do not prove them, and proof
  needs a live call outside E10 scope.
- Per-run subscription usage metering: no meaningful dollar meter is
  established; the first run is bounded by steps/time/stop instead.
- Separately billed Jev limits: `TYPESAFE_API_KEY` is set in this
  inspection shell (shell-only observation, not app-runtime proof);
  no no-call method verifies the account plan or limits.
- The 1.3.0 → 1.4.0 protocol delta against the frozen contract.

## Blocked facts

- E10-B1 · CLI/contract version mismatch: installed `1.4.0` vs pinned
  `1.3.0`. Readiness reports `muse_version_mismatch` and the path is
  unavailable. Resolution needs an explicit M contract re-pin with
  schema/tool-inventory re-verification, never a silent fallback.
- E10-B2 · Live entitlement proof belongs to the E11 authorization,
  not to this no-spend gate.
- E10-B3 · Separately billed Jev/retrieval limits are unverified and
  need their own verified limits plus owner authorization.

## First-run proposal (unchanged, still unauthorized)

One adaptive public search targeting 30–45 minutes, stopping by the
approved limit (proposed 45 minutes, the frozen ceiling), saving
checkpoints and receipts as it goes, never auto-repeating, with
honest no-result coverage keeping RW-G1 open. No live call or spend
is authorized by this record.
