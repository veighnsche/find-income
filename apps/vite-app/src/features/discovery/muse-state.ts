// E06 INTEGRATION INTERFACE — M implements the real client to match this file.
//
// Until E06 publishes the real HTTP/client surface, the recruitment journey UI
// builds against this narrow view-model with fixture data (zero network/model
// calls). At E06, M keeps these exact type names, states, codes and outcomes
// and swaps the fixture source for live reads:
//
//   M MUST PROVIDE (per tier: contributor, standard):
//   - GET readiness -> MuseReadiness { state, code, detail, tier }
//     Codes mirror musecode.Check exactly: muse_ready, muse_not_configured,
//     muse_version_mismatch, muse_lane_unverified, muse_protocol_unverified,
//     muse_workspace_unverified. No other codes; unknown fails closed.
//   - GET run checkpoints -> RunCheckpoint[] (durable cursors/receipts only)
//   - GET run report -> MuseRunReport | null (partial/no-result safe)
//   - commissionedCalls: count of owner-explicit commissions this session
//
//   M MUST PRESERVE:
//   - Page load and passive reads commission NOTHING (no POST/session input).
//   - readinessStateFor mapping below (ready/unavailable/needs-setup).
//   - MuseOutcome values: completed/stopped/failed/crashed/expired.
//   - useMuseState signature: (scenario?: MuseFixtureScenario) => MuseJourneyState.
//     M may ignore the scenario argument once live; fixtures remain for tests.
//
// Owner-facing rules: surface real readiness per tier, saved checkpoints,
// partial/no-result reports and one clear next action. A blocked tier disables
// its commissions (Find jobs, Check) with the code + detail as the reason.
// Standard readiness gates Prepare drafting only; exact owner edits, Answer
// prefill display and Review/send never need a model call.

import { useMemo } from "react"

// MuseTier mirrors musecode.Tier: contributor sessions are public-only,
// standard sessions are private preparation-only.
export type MuseTier = "contributor" | "standard"

// MuseReadinessCode mirrors the musecode.Check status codes exactly.
// Any code outside this union is treated as unavailable (fail closed).
export type MuseReadinessCode =
  | "muse_ready"
  | "muse_not_configured"
  | "muse_version_mismatch"
  | "muse_lane_unverified"
  | "muse_protocol_unverified"
  | "muse_workspace_unverified"

// MuseReadinessState is the owner-visible summary of one tier.
export type MuseReadinessState = "ready" | "unavailable" | "needs-setup"

export interface MuseReadiness {
  state: MuseReadinessState
  code: MuseReadinessCode
  detail: string
  tier: MuseTier
}

// readinessStateFor maps a frozen musecode code to its owner-visible state.
// Only muse_ready is ready; a missing configuration needs setup; every other
// code (including unknown future codes) is unavailable.
export function readinessStateFor(code: string): MuseReadinessState {
  switch (code) {
    case "muse_ready":
      return "ready"
    case "muse_not_configured":
      return "needs-setup"
    default:
      return "unavailable"
  }
}

// readinessFor builds one tier verdict from a code + detail pair.
export function readinessFor(
  tier: MuseTier,
  code: MuseReadinessCode,
  detail: string
): MuseReadiness {
  return { state: readinessStateFor(code), code, detail, tier }
}

// MuseOutcome mirrors musecode.Outcome. Only completed with validated saves
// counts as success; every other outcome keeps its gate open.
export type MuseOutcome =
  | "completed"
  | "stopped"
  | "failed"
  | "crashed"
  | "expired"

// RunCheckpoint is one durable run checkpoint: what was saved so far, not a
// promise of more. Mirrors the durable cursor/receipt record.
export interface RunCheckpoint {
  runRef: string
  tier: MuseTier
  savedCount: number
  lastSavedReceipt: string
  savedRefs: string[]
  updatedAt: string
}

// MuseRunReport is the honest terminal (or so-far) report for one run.
// Partial runs list real checkpoints plus gaps; no-result runs list coverage
// with empty savedRefs — never invented vacancies.
export interface MuseRunReport {
  runRef: string
  tier: MuseTier
  outcome: MuseOutcome
  detail: string
  savedRefs: string[]
  searched: string[]
  reused: string[]
  gaps: string[]
  nextAction: string
}

// MuseJourneyState is the whole narrow view-model one surface needs.
export interface MuseJourneyState {
  contributor: MuseReadiness
  standard: MuseReadiness
  checkpoints: RunCheckpoint[]
  report: MuseRunReport | null
  // Owner-explicit commissions this session. Fixtures always report 0 and
  // perform zero network/model calls; the live client counts real POSTs.
  commissionedCalls: number
}

export type MuseFixtureScenario =
  | "ready"
  | "needs-setup"
  | "blocked"
  | "partial"
  | "no-result"
  | "long-list"

function checkpoint(
  runRef: string,
  index: number,
  savedCount: number
): RunCheckpoint {
  const refs = Array.from(
    { length: savedCount },
    (_, slot) => `vacancy-${runRef}-${index}-${slot + 1}`
  )
  return {
    runRef,
    tier: "contributor",
    savedCount,
    lastSavedReceipt: refs.length === 0 ? "" : refs[refs.length - 1],
    savedRefs: refs,
    updatedAt: `2026-09-24T10:${String(10 + index).padStart(2, "0")}:00Z`,
  }
}

// fixtureMuseState returns deterministic fixture data per scenario. Pure:
// no I/O, no randomness, commissionedCalls always 0.
export function fixtureMuseState(
  scenario: MuseFixtureScenario = "ready"
): MuseJourneyState {
  const contributorReady = readinessFor(
    "contributor",
    "muse_ready",
    "muse contributor session may be admitted"
  )
  const standardReady = readinessFor(
    "standard",
    "muse_ready",
    "muse standard session may be admitted"
  )
  switch (scenario) {
    case "needs-setup":
      return {
        contributor: readinessFor(
          "contributor",
          "muse_not_configured",
          "muse CLI path is not configured"
        ),
        standard: readinessFor(
          "standard",
          "muse_not_configured",
          "muse CLI path is not configured"
        ),
        checkpoints: [],
        report: null,
        commissionedCalls: 0,
      }
    case "blocked":
      return {
        contributor: readinessFor(
          "contributor",
          "muse_lane_unverified",
          "effective model/subscription lane is not verified"
        ),
        standard: readinessFor(
          "standard",
          "muse_protocol_unverified",
          "initialize sessionMcp + config.mcpServers is not verified"
        ),
        checkpoints: [],
        report: null,
        commissionedCalls: 0,
      }
    case "partial":
      return {
        contributor: contributorReady,
        standard: standardReady,
        checkpoints: [checkpoint("run-partial", 0, 2), checkpoint("run-partial", 1, 1)],
        report: {
          runRef: "run-partial",
          tier: "contributor",
          outcome: "stopped",
          detail: "stopped by the owner after two checkpoints",
          savedRefs: ["vacancy-run-partial-0-1", "vacancy-run-partial-0-2"],
          searched: ["example.com/jobs", "careers.example.dev"],
          reused: ["vacancy-run-partial-0-1"],
          gaps: ["Berlin on-site roles not yet covered"],
          nextAction:
            "Review the 2 saved roles below, then Find more jobs or Check chosen jobs.",
        },
        commissionedCalls: 0,
      }
    case "no-result":
      return {
        contributor: contributorReady,
        standard: standardReady,
        checkpoints: [],
        report: {
          runRef: "run-empty",
          tier: "contributor",
          outcome: "completed",
          detail: "completed with no suitable vacancy",
          savedRefs: [],
          searched: ["example.com/jobs", "careers.example.dev", "jobs.example.org"],
          reused: [],
          gaps: [
            "No remote backend roles in Berlin matched the saved brief",
            "Two postings lacked salary details and were held, not saved",
          ],
          nextAction:
            "No suitable vacancy appeared. Change my search to widen the brief, or run Find jobs again later.",
        },
        commissionedCalls: 0,
      }
    case "long-list":
      return {
        contributor: contributorReady,
        standard: standardReady,
        checkpoints: Array.from({ length: 30 }, (_, index) =>
          checkpoint("run-long", index, 2)
        ),
        report: null,
        commissionedCalls: 0,
      }
    case "ready":
    default:
      return {
        contributor: contributorReady,
        standard: standardReady,
        checkpoints: [],
        report: null,
        commissionedCalls: 0,
      }
  }
}

// useMuseState is the fixture hook the journey builds against until E06.
// It performs zero network/model calls on every render; M replaces the body
// with live GET reads behind this same signature.
export function useMuseState(
  scenario: MuseFixtureScenario = "ready"
): MuseJourneyState {
  return useMemo(() => fixtureMuseState(scenario), [scenario])
}

// describeReadiness renders one truthful owner-facing readiness line.
export function describeReadiness(readiness: MuseReadiness): string {
  const tierLabel =
    readiness.tier === "contributor" ? "Contributor" : "Standard"
  switch (readiness.state) {
    case "ready":
      return `${tierLabel}: ready (${readiness.code}).`
    case "needs-setup":
      return `${tierLabel}: needs setup (${readiness.code}) — ${readiness.detail}.`
    case "unavailable":
      return `${tierLabel}: unavailable (${readiness.code}) — ${readiness.detail}.`
  }
}

// nextActionFor derives the single clearest next action from journey state.
export function nextActionFor(state: MuseJourneyState): string {
  if (state.contributor.state !== "ready")
    return `Find jobs and Check are blocked: ${state.contributor.detail}.`
  if (state.report !== null) return state.report.nextAction
  if (state.checkpoints.length > 0)
    return "Review the saved checkpoints below, then Check chosen jobs."
  return "Press Find jobs to start a bounded discovery run."
}
