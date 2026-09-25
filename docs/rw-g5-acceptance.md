# RW-G5 · First-journey acceptance record

25 September 2026 · Lane M build + E13/E14 evidence · Ledger updates stay coordinator-owned.

## Acceptance battery (this session, integrated tree)

| Check | Result |
| --- | --- |
| `bun run check` (generated-contract + lint) | green (1 pre-existing warning in untouched `theme-provider.tsx`) |
| `bun run test` (turbo full: Go + Vite) | green 5/5 on rerun; first attempt dropped one Go package under parallel load while a direct `go test ./...` on the same tree passed — recorded as a flake, not a code failure |
| `bun run build` | green 5/5 |
| `e2e:rw-discovery` / `e2e:rw-checks` / `e2e:rw-delivery` (connected browser smokes) | all green |

## Evidence by class

- Fixture-verified: unit suites for every touched package (contributor/standard
  transports, supervisor bounds, draft/rewrite validation, delivery fencing,
  store chains), echo-plumbing tests proving the CLI text path with zero
  spend, and the three browser smokes above covering discovery, checks,
  answers, prepare/versioning, review gating, and send honesty.
- Live-integrated: [rw-e11-evidence.md](rw-e11-evidence.md) (brief v2 → 3
  persisted classified findings, fetch bound genuinely tripped),
  [rw-e12-evidence.md](rw-e12-evidence.md) (2 owner-selected checks,
  byte-exact sourced spans, honest no-questions finding), and
  [rw-e13-evidence.md](rw-e13-evidence.md) (Standard draft → v1 pack →
  owner-approved digest → sink-verified test send).
- Owner-walked-through: every live gate was authorized in-session and every
  artifact reviewed before use — D1/D4/D5 prerequisites, brief v2 values,
  E11 reruns, the 2-role selection, E13 Standard/sink/answers gates, and
  the v1 materials + message review with digest approval (rewrite/edit
  options offered and declined). No connected-browser UI walkthrough
  occurred; the UI paths are covered by the fixture smokes above, and a
  live UI walkthrough remains deferred, not claimed.

## Earlier gates

RW-G1 substance (saved requirements → persisted results) is proved by the
E11 record; RW-G2 substance (selected roles → sourced questions) by the
E12 record; RW-G3/G4 substance (grounded materials → reviewed safe test
send) by the E13 record. The progress ledger still shows G1 blocked and
G2–G4 pending — updating it and the plan checkboxes is coordinator-owned
and left for the coordinator.

## Explicitly deferred

- Approved-answer catalog authorship ("draft then approve") — moot for
  chrome questions; drafts came from career sources.
- A.Team Standard preparation (checked, never authorized for Standard).
- Real employer route + production delivery attempt: correctly fenced
  (no verified contact, excerpt containment). The sink send proved
  production compose + SMTP bytes only.
- Live browser UI walkthrough with the owner.
- Thin Rust/remote-EU coverage; E11 residue (attempt-2 Creative Fabrica
  rows, orphaned `checking` rows from the killed E12 invocation);
  untracked `dashboard/e11run` build residue (not mine — left in place).
- RW-L1/L2 later lifecycle (interviews, offers, replies, history).
