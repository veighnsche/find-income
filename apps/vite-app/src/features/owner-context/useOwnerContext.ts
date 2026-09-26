import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  getPreferences,
  getRound,
  getSearchBrief,
  isUnauthenticated,
  processInput,
  RequestError,
  type Preferences,
  type Round,
  type SearchBriefView,
} from "@/api/client"
import { useSession } from "@/api/session"
import { useSavedGoals } from "@/components/shared/saved-goals"
import { useRead } from "@/pages/useRead"

export type RoleCriterion = Preferences["roleCriteria"][number]
export type BriefFact = SearchBriefView["facts"][number]

export const ownerContextPendingKey = "jobseek.owner-context-pending"
export const ownerContextPollIntervalMs = 3000

/** Effective saved context: what the next explicit Find jobs run would use. */
export interface EffectiveSavedContext {
  /** Current saved profile version (PreferencesResponse.version). */
  profileVersion: number
  /** Effective brief identity, null until the first brief is authored. */
  rubricVersion: string | null
  rubricSource: string | null
  /** Human-readable one-line summary for headers. */
  summary: string
  /** Full saved rows for in-context rendering. */
  preferences: Preferences
  /** Preference-derived facts from SearchBriefView.facts (NOT verified CV facts). */
  briefFacts: BriefFact[]
  requirements: RoleCriterion[]
  /** Present only when a catalog was authored for this brief version. */
  catalogVersion: string | null
  /** True when brief.profileVersion !== preferences.version. */
  briefStale: boolean
}

export type CorrectionStatus =
  | { kind: "idle" }
  | { kind: "sending"; baseVersion: number; targetText: string }
  | {
      kind: "pending"
      roundId: string
      requestKey: string
      baseVersion: number
      roundState: Round["state"]
      targetText: string
    }
  | {
      kind: "saved"
      roundId: string
      requestKey: string
      /** Version read back AFTER completion (never assumed). */
      savedVersion: number
      targetText: string
    }
  | {
      kind: "conflict"
      baseVersion: number
      /** Freshly re-read effective version. */
      currentVersion: number
      message: string
    }
  | { kind: "error"; message: string; retryable: boolean }

export interface OwnerContextValue {
  /** GET-only reads; loading/error follow the existing useRead convention. */
  context: EffectiveSavedContext | null
  contextState: "loading" | "ready" | "error"
  contextError: string | null
  retryContext: () => void
  /** Correction state machine (B owns transitions; C reads readiness). */
  correction: CorrectionStatus
  /**
   * File a natural-language correction against the current saved version.
   * Builds ProcessInputRequest {targetKind:"profile", targetId:"current",
   * expectedRevision, text, requestKey}. No-op while sending/pending or when
   * the text is blank. Accepted is never presented as saved.
   */
  submitCorrection: (text: string) => Promise<void>
  /** Re-read round + preferences; transitions pending toward saved/conflict/error. */
  refreshCorrection: () => Promise<void>
  /** Forget terminal/failed state after the owner acknowledges it. */
  dismissCorrection: () => void
  /** Lane C readiness gate: Find jobs enabled only when true. */
  discoveryReady: boolean
  /** Plain-language reason when discoveryReady is false. */
  discoveryBlockedReason: string | null
}

function requestMessage(cause: unknown): string {
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

/**
 * Stable idempotency key per distinct (baseVersion + normalized text), so a
 * retry replays the same round while new wording commissions a new one.
 */
export function correctionRequestKey(
  baseVersion: number,
  text: string
): string {
  const normalized = text.trim().replace(/\s+/g, " ")
  let hash = 0x811c9dc5
  for (let index = 0; index < normalized.length; index += 1) {
    hash ^= normalized.charCodeAt(index)
    hash = Math.imul(hash, 0x01000193)
  }
  return `profile-correction-v${baseVersion}-${(hash >>> 0).toString(16).padStart(8, "0")}`
}

/** One-line saved summary. Work pattern is derived from the two booleans. */
export function buildSavedSummary(preferences: Preferences): string {
  const pattern =
    preferences.allowRemote && preferences.allowHybrid
      ? "remote + hybrid ok"
      : preferences.allowRemote
        ? "remote ok"
        : preferences.allowHybrid
          ? "hybrid ok"
          : "onsite only"
  const location =
    preferences.preferredLocation === ""
      ? "no location set"
      : preferences.preferredLocation
  const count = preferences.roleCriteria.length
  return `Profile v${preferences.version} · ${location} · ${pattern} · ${count} ${count === 1 ? "criterion" : "criteria"}`
}

interface PersistedPending {
  roundId: string
  requestKey: string
  baseVersion: number
  targetText: string
}

function loadPersistedPending(
  storage: Pick<Storage, "getItem">
): Extract<CorrectionStatus, { kind: "pending" }> | null {
  try {
    const raw = storage.getItem(ownerContextPendingKey)
    if (raw === null || raw === "") return null
    const parsed = JSON.parse(raw) as Partial<PersistedPending>
    if (typeof parsed.roundId !== "string" || parsed.roundId === "")
      return null
    if (typeof parsed.requestKey !== "string" || parsed.requestKey === "")
      return null
    if (
      typeof parsed.baseVersion !== "number" ||
      !Number.isInteger(parsed.baseVersion) ||
      parsed.baseVersion < 1
    )
      return null
    if (typeof parsed.targetText !== "string" || parsed.targetText === "")
      return null
    return {
      kind: "pending",
      roundId: parsed.roundId,
      requestKey: parsed.requestKey,
      baseVersion: parsed.baseVersion,
      roundState: "queued",
      targetText: parsed.targetText,
    }
  } catch {
    return null
  }
}

function persistPending(
  storage: Pick<Storage, "setItem">,
  pending: PersistedPending
): void {
  try {
    storage.setItem(ownerContextPendingKey, JSON.stringify(pending))
  } catch {
    // Private-mode storage failures must not break the correction flow.
  }
}

function clearPersistedPending(
  storage: Pick<Storage, "removeItem">
): void {
  try {
    storage.removeItem(ownerContextPendingKey)
  } catch {
    // Private-mode storage failures must not break the correction flow.
  }
}

function isTerminalRoundState(state: Round["state"]): boolean {
  return state === "completed" || state === "failed"
}

function pendingRoundDescription(roundState: Round["state"]): string {
  switch (roundState) {
    case "paused":
      return "paused"
    case "awaiting_input":
      return "waiting for input"
    case "stopping":
      return "stopping"
    case "running":
      return "running"
    case "queued":
      return "queued"
    default:
      return roundState
  }
}

// Saved context reads plus the profile-correction state machine. Mount and
// navigation only issue GETs; submitCorrection is the sole commissioning path
// and every saved/conflict transition is confirmed by a fresh preferences
// readback, never by the accepted round alone.
//
// G1: the preferences half is F's single shared saved-goal read
// (SavedGoalsProvider/useSavedGoals), so every surface on the page —
// Search, discovery, grouped jobs — gates on the same accepted version and
// a save refreshes Find jobs immediately (R02). The brief read stays local
// (subscribed to the "run" scope: commissions author the brief) and the
// correction machine is unchanged. Must render under SavedGoalsProvider.
export function useOwnerContext(): OwnerContextValue {
  const { session, loseSession } = useSession()
  const savedGoals = useSavedGoals()
  const briefRead = useRead<SearchBriefView | null>(
    "owner-context:brief",
    (signal) =>
      getSearchBrief(signal).catch((cause: unknown) => {
        // No authored brief yet is normal before the first run, not an error.
        if (cause instanceof RequestError && cause.status === 404) return null
        throw cause
      }),
    { scopes: ["run"] }
  )

  const [correction, setCorrectionState] = useState<CorrectionStatus>(() => {
    if (typeof window === "undefined" || window.localStorage === undefined)
      return { kind: "idle" }
    return loadPersistedPending(window.localStorage) ?? { kind: "idle" }
  })
  const correctionRef = useRef<CorrectionStatus | null>(null)
  if (correctionRef.current === null) correctionRef.current = correction
  const submittingRef = useRef(false)
  const refreshingRef = useRef(false)

  const updateCorrection = useCallback((next: CorrectionStatus) => {
    correctionRef.current = next
    setCorrectionState(next)
  }, [])

  const retryContext = useCallback(() => {
    savedGoals.retry()
    briefRead.retry()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- retry callbacks are stable; the read objects are not
  }, [savedGoals.retry, briefRead.retry])

  const resolveTerminalRound = useCallback(
    async (
      round: Round,
      pending: Extract<CorrectionStatus, { kind: "pending" }>
    ) => {
      if (round.state === "failed") {
        clearPersistedPending(window.localStorage)
        updateCorrection({
          kind: "error",
          message: `The correction round failed before saving (round ${round.id}). Nothing was saved; try again with the same or new wording.`,
          retryable: true,
        })
        return
      }
      let readback: Preferences
      try {
        readback = await getPreferences()
      } catch (cause) {
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        updateCorrection({
          kind: "error",
          message:
            "The correction finished but the saved result could not be read back. Check again before assuming anything was saved.",
          retryable: true,
        })
        return
      }
      if (
        readback.version > pending.baseVersion &&
        readback.version === round.profileVersion
      ) {
        clearPersistedPending(window.localStorage)
        retryContext()
        updateCorrection({
          kind: "saved",
          roundId: round.id,
          requestKey: pending.requestKey,
          savedVersion: readback.version,
          targetText: pending.targetText,
        })
        return
      }
      if (readback.version > pending.baseVersion) {
        clearPersistedPending(window.localStorage)
        retryContext()
        updateCorrection({
          kind: "conflict",
          baseVersion: pending.baseVersion,
          currentVersion: readback.version,
          message: `The profile is now version ${readback.version}, but this correction saved version ${round.profileVersion}. Something else changed the goals first; review the current goals and send the correction again.`,
        })
        return
      }
      clearPersistedPending(window.localStorage)
      updateCorrection({
        kind: "error",
        message: `The correction finished but the saved profile is still version ${pending.baseVersion}. Nothing changed; reword the correction and send it again.`,
        retryable: false,
      })
    },
    [loseSession, retryContext, updateCorrection]
  )

  const refreshCorrection = useCallback(async () => {
    const current = correctionRef.current
    if (current === null || current.kind !== "pending") return
    if (refreshingRef.current) return
    refreshingRef.current = true
    try {
      const round = await getRound(current.roundId)
      if (isTerminalRoundState(round.state)) {
        await resolveTerminalRound(round, current)
        return
      }
      if (round.state !== current.roundState)
        updateCorrection({ ...current, roundState: round.state })
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      if (cause instanceof RequestError && cause.status === 404) {
        clearPersistedPending(window.localStorage)
        updateCorrection({
          kind: "error",
          message: `The correction round ${current.roundId} is no longer known to the server. Nothing is confirmed saved; send the correction again.`,
          retryable: true,
        })
        return
      }
      // Transient poll failures keep pending tracking; the next poll or a
      // manual check continues toward the terminal state.
    } finally {
      refreshingRef.current = false
    }
  }, [loseSession, resolveTerminalRound, updateCorrection])

  const submitCorrection = useCallback(
    async (text: string) => {
      const current = correctionRef.current
      if (
        submittingRef.current ||
        current === null ||
        current.kind === "sending" ||
        current.kind === "pending"
      )
        return
      const trimmed = text.trim()
      if (trimmed === "") return
      if (savedGoals.state !== "ready" || savedGoals.goals === null) {
        updateCorrection({
          kind: "error",
          message:
            "The saved profile is not loaded yet. Reload the saved profile and try again.",
          retryable: false,
        })
        return
      }
      if (session === undefined) {
        updateCorrection({
          kind: "error",
          message: "Checking dashboard session…",
          retryable: true,
        })
        return
      }
      if (session === null) {
        updateCorrection({
          kind: "error",
          message: "Sign in to send a correction.",
          retryable: false,
        })
        return
      }
      const baseVersion = savedGoals.goals.version
      const requestKey = correctionRequestKey(baseVersion, trimmed)
      submittingRef.current = true
      updateCorrection({ kind: "sending", baseVersion, targetText: trimmed })
      try {
        const response = await processInput(
          {
            requestKey,
            targetKind: "profile",
            targetId: "current",
            expectedRevision: baseVersion,
            text: trimmed,
          },
          session.csrfToken
        )
        const round = response.round
        const pending: Extract<CorrectionStatus, { kind: "pending" }> = {
          kind: "pending",
          roundId: round.id,
          requestKey,
          baseVersion,
          roundState: round.state,
          targetText: trimmed,
        }
        persistPending(window.localStorage, {
          roundId: round.id,
          requestKey,
          baseVersion,
          targetText: trimmed,
        })
        if (isTerminalRoundState(round.state)) {
          await resolveTerminalRound(round, pending)
          return
        }
        updateCorrection(pending)
      } catch (cause) {
        if (isUnauthenticated(cause)) {
          loseSession()
          updateCorrection({
            kind: "error",
            message: "The dashboard session expired before the correction was sent.",
            retryable: false,
          })
          return
        }
        if (cause instanceof RequestError && cause.status === 409) {
          let fresh: Preferences
          try {
            fresh = await getPreferences()
          } catch (readCause) {
            if (isUnauthenticated(readCause)) {
              loseSession()
              return
            }
            updateCorrection({
              kind: "error",
              message:
                "The profile changed elsewhere and the current version could not be read. Reload and try again.",
              retryable: true,
            })
            return
          }
          retryContext()
          updateCorrection({
            kind: "conflict",
            baseVersion,
            currentVersion: fresh.version,
            message:
              fresh.version === baseVersion
                ? `The correction was rejected as a conflicting change against profile version ${baseVersion}. The same wording may already be in flight; wait for any pending correction before sending it again.`
                : `The saved profile moved before this correction was accepted (based on version ${baseVersion}, now version ${fresh.version}). Review the current goals and send the correction again.`,
          })
          return
        }
        if (cause instanceof RequestError && cause.status === 503) {
          updateCorrection({
            kind: "error",
            message:
              "The correction runner is not ready. Nothing was accepted; try again once setup is complete.",
            retryable: true,
          })
          return
        }
        if (cause instanceof RequestError && cause.status === 400) {
          updateCorrection({
            kind: "error",
            message: cause.message,
            retryable: false,
          })
          return
        }
        updateCorrection({
          kind: "error",
          message: requestMessage(cause),
          retryable: true,
        })
      } finally {
        submittingRef.current = false
      }
    },
    [
      loseSession,
      savedGoals.goals,
      savedGoals.state,
      resolveTerminalRound,
      retryContext,
      session,
      updateCorrection,
    ]
  )

  const dismissCorrection = useCallback(() => {
    const current = correctionRef.current
    if (
      current === null ||
      (current.kind !== "saved" &&
        current.kind !== "conflict" &&
        current.kind !== "error")
    )
      return
    clearPersistedPending(window.localStorage)
    updateCorrection({ kind: "idle" })
  }, [updateCorrection])

  // A reloaded page resumes tracking an accepted-but-unconfirmed correction
  // instead of forgetting it or calling it saved.
  const restorePendingRef = useRef(correction.kind === "pending")
  useEffect(() => {
    if (!restorePendingRef.current) return
    restorePendingRef.current = false
    void refreshCorrection()
  }, [refreshCorrection])

  const polling = correction.kind === "pending"
  useEffect(() => {
    if (!polling) return
    const timer = window.setInterval(() => {
      void refreshCorrection()
    }, ownerContextPollIntervalMs)
    return () => window.clearInterval(timer)
  }, [polling, refreshCorrection])

  const contextState: OwnerContextValue["contextState"] =
    savedGoals.state === "error" || briefRead.status === "error"
      ? "error"
      : savedGoals.state === "ready" && briefRead.status === "ready"
        ? "ready"
        : "loading"
  const contextError =
    savedGoals.state === "error"
      ? savedGoals.error
      : briefRead.status === "error"
        ? briefRead.error
        : null

  const context: EffectiveSavedContext | null = useMemo(() => {
    if (savedGoals.state !== "ready" || savedGoals.goals === null)
      return null
    if (briefRead.status !== "ready") return null
    const preferences = savedGoals.goals
    const brief = briefRead.data
    return {
      profileVersion: preferences.version,
      rubricVersion: brief?.rubricVersion ?? null,
      rubricSource: brief?.rubricSource ?? null,
      summary: buildSavedSummary(preferences),
      preferences,
      briefFacts: brief?.facts ?? [],
      requirements: preferences.roleCriteria,
      catalogVersion: brief?.catalogVersion ?? null,
      briefStale: brief !== null && brief.profileVersion !== preferences.version,
    }
  }, [briefRead.data, briefRead.status, savedGoals.goals, savedGoals.state])

  const readiness = useMemo<{
    ready: boolean
    reason: string | null
  }>(() => {
    if (context === null || contextState !== "ready")
      return {
        ready: false,
        reason:
          contextState === "error" && contextError !== null
            ? contextError
            : "Search context is still loading.",
      }
    if (context.briefStale)
      return {
        ready: false,
        reason: `The search brief is behind profile version ${context.profileVersion}. Reload the saved context before finding jobs.`,
      }
    if (context.requirements.length === 0)
      return {
        ready: false,
        reason:
          "No wants or don't-wants are saved yet. Save at least one explicit choice above before finding jobs.",
      }
    switch (correction.kind) {
      case "idle":
      case "saved":
        return { ready: true, reason: null }
      case "sending":
        return { ready: false, reason: "Sending your change…" }
      case "pending":
        return correction.roundState === "paused"
          ? {
              ready: false,
              reason:
                "Your accepted change is paused. Check its status on My search before finding jobs.",
            }
          : {
              ready: false,
              reason: `Saving your change (${pendingRoundDescription(correction.roundState)})… Find jobs unlocks once the new version is confirmed saved.`,
            }
      case "conflict":
        return { ready: false, reason: correction.message }
      case "error":
        return { ready: false, reason: correction.message }
    }
  }, [context, contextError, contextState, correction])

  return {
    context,
    contextState,
    contextError,
    retryContext,
    correction,
    submitCorrection,
    refreshCorrection,
    dismissCorrection,
    discoveryReady: readiness.ready,
    discoveryBlockedReason: readiness.reason,
  }
}
