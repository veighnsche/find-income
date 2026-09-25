// E06 INTEGRATION INTERFACE — live client published by M against this file.
//
// Type names, states, codes and outcomes below are frozen. The journey reads
// the view-model through useMuseState: production pages pass "live" for real
// GET reads (readiness per tier + commission count; zero POSTs), while an
// explicit fixture scenario keeps deterministic data for tests.
//
//   LIVE SURFACE (per tier: contributor, standard):
//   - GET readiness -> MuseReadiness { state, code, detail, tier }
//     Codes mirror musecode.Check exactly: muse_ready, muse_not_configured,
//     muse_version_mismatch, muse_lane_unverified, muse_protocol_unverified,
//     muse_workspace_unverified. Unknown codes fail closed to unavailable.
//   - GET run checkpoints -> RunCheckpoint[] (durable cursors/receipts only)
//   - GET run report -> MuseRunReport | null (partial/no-result safe)
//   - commissionedCalls: count of owner-explicit commissions this session
//
// Run checkpoints/report stay empty until run selection arrives with the
// first authorized commission (E11): there is no run to show yet, and the
// hook never invents one.
//
//   PRESERVED INVARIANTS:
//   - Page load and passive reads commission NOTHING (no POST/session input).
//   - readinessStateFor mapping below (ready/unavailable/needs-setup).
//   - MuseOutcome values: completed/stopped/failed/crashed/expired.
//   - Fixture scenarios keep byte-identical deterministic data for tests.
//
// Owner-facing rules: surface real readiness per tier, saved checkpoints,
// partial/no-result reports and one clear next action. A blocked tier disables
// its commissions (Find jobs, Check) with the code + detail as the reason.
// Standard readiness gates Prepare drafting only; exact owner edits, Answer
// prefill display and Review/send never need a model call.

import { useEffect, useMemo, useState } from "react"

import {
  getMuseCommissions,
  getMuseReadiness,
  type MuseReadiness as ApiReadiness,
} from "../../api/client"

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

// MuseScenario selects the hook source: "live" reads the real API with GETs
// only, while an explicit fixture scenario keeps deterministic test data.
export type MuseScenario = MuseFixtureScenario | "live"

// LiveMuseReads is one successful live read round: per-tier readiness plus
// the owner-explicit commission count. Checkpoints/report need a selected
// run, which arrives with the first authorized commission (E11).
export interface LiveMuseReads {
  contributor: ApiReadiness
  standard: ApiReadiness
  commissionedCalls: number
}

// liveJourneyFromReads maps one live read round onto the frozen view-model.
// Unknown readiness codes fail closed to unavailable; the code echo stays
// verbatim so the owner sees what the API actually reported.
export function liveJourneyFromReads(reads: LiveMuseReads): MuseJourneyState {
  const view = (api: ApiReadiness, tier: MuseTier): MuseReadiness => ({
    state: readinessStateFor(api.code),
    code: api.code as MuseReadinessCode,
    detail: api.detail,
    tier,
  })
  return {
    contributor: view(reads.contributor, "contributor"),
    standard: view(reads.standard, "standard"),
    checkpoints: [],
    report: null,
    commissionedCalls: reads.commissionedCalls,
  }
}

function loadingJourney(): MuseJourneyState {
  return {
    contributor: readinessFor(
      "contributor",
      "muse_not_configured",
      "Reading local Muse readiness…"
    ),
    standard: readinessFor(
      "standard",
      "muse_not_configured",
      "Reading local Muse readiness…"
    ),
    checkpoints: [],
    report: null,
    commissionedCalls: 0,
  }
}

function unreachableJourney(detail: string): MuseJourneyState {
  return {
    contributor: readinessFor(
      "contributor",
      "muse_not_configured",
      `Could not reach the API: ${detail}`
    ),
    standard: readinessFor(
      "standard",
      "muse_not_configured",
      `Could not reach the API: ${detail}`
    ),
    checkpoints: [],
    report: null,
    commissionedCalls: 0,
  }
}

// useMuseState serves the journey view-model. Production pages pass "live"
// for real GET reads (readiness per tier + commission count; zero POSTs, so
// passive reads commission nothing). An explicit fixture scenario returns
// deterministic data with zero network calls. The hook never throws: read
// failures surface as needs-setup states carrying the real detail.
export function useMuseState(scenario: MuseScenario = "ready"): MuseJourneyState {
  const fixture = useMemo(
    () => fixtureMuseState(scenario === "live" ? "ready" : scenario),
    [scenario]
  )
  const [live, setLive] = useState<MuseJourneyState>(loadingJourney)
  useEffect(() => {
    if (scenario !== "live") return
    const controller = new AbortController()
    let cancelled = false
    const load = async () => {
      try {
        const [contributor, standard, commissionedCalls] = await Promise.all([
          getMuseReadiness("contributor", controller.signal),
          getMuseReadiness("standard", controller.signal),
          getMuseCommissions(controller.signal),
        ])
        if (!cancelled) {
          setLive(
            liveJourneyFromReads({ contributor, standard, commissionedCalls })
          )
        }
      } catch (cause) {
        if (cancelled || controller.signal.aborted) return
        const detail =
          cause instanceof Error ? cause.message : "Unknown read failure."
        if (!cancelled) setLive(unreachableJourney(detail))
      }
    }
    void load()
    return () => {
      cancelled = true
      controller.abort()
    }
  }, [scenario])
  return scenario === "live" ? live : fixture
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
