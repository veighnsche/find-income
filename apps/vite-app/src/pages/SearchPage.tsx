import { useEffect, useRef, useState } from "react"
import { listResearchRuns, type RunHistoryItem } from "@/api/client"
import { useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  notifyGoalsAccepted,
  SavedGoalsProvider,
  useSavedGoals,
} from "@/components/shared"
import {
  SEVEN_STAGES,
  StageProgress,
  type StageInput,
} from "@/components/shared/stage-progress"
import { ExperiencePanel } from "@/features/goals/ExperiencePanel"
import { WantsPanel } from "@/features/goals/WantsPanel"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { DiscoverySection } from "@/features/discovery/discovery-section"
import {
  useOwnerContext,
  type EffectiveSavedContext,
} from "@/features/owner-context/useOwnerContext"
import { useRead } from "@/pages/useRead"
import { useRoute } from "@/routes/useRoute"

const STAGE_ACTORS: Record<string, string> = {
  goals: "You",
  find: "Contributor + Jev",
  select: "You",
  check: "Contributor",
  answer: "You + Jev",
  prepare: "Standard",
  handoff: "You",
}

const ACTIVE_RUN_STATES: ReadonlySet<string> = new Set([
  "queued",
  "running",
  "awaiting_input",
  "stopping",
  "paused",
])

/**
 * Newest relevant run state from newest-first history: active work wins
 * (the search is in Find jobs), else the newest terminal state. Unknown
 * states and empty/unreadable history yield null (no known run).
 */
export function newestRunState(items: RunHistoryItem[]): string | null {
  const active = items.find((item) => ACTIVE_RUN_STATES.has(item.state))
  if (active !== undefined) return active.state
  const terminal = items.find(
    (item) => item.state === "completed" || item.state === "failed"
  )
  return terminal?.state ?? null
}

/**
 * My-search stage orientation from actual state (F2/R17): saved goals
 * complete step 1, a running/paused/failed search rests on Find jobs,
 * and completed results move to Select jobs. Unknown goals keep the
 * conservative Your-goals marker; unknown runs keep Find jobs as the
 * next step once goals exist.
 */
export function searchStageView({
  goalsSaved,
  runState,
}: {
  goalsSaved: boolean | null
  runState: string | null
}): { stages: StageInput[]; activeStageId: string } {
  let active = "goals"
  let completedThrough = -1
  if (goalsSaved === true) {
    if (runState === "completed") {
      active = "select"
      completedThrough = 1
    } else {
      active = "find"
      completedThrough = 0
    }
  }
  return {
    stages: SEVEN_STAGES.map((stage, index) => ({
      ...stage,
      state: index <= completedThrough ? ("complete" as const) : ("upcoming" as const),
      actor: STAGE_ACTORS[stage.id] ?? "",
    })),
    activeStageId: active,
  }
}

function ProfileFactsPanel({ context }: { context: EffectiveSavedContext }) {
  return (
    <section aria-labelledby="profile-facts-heading">
      <h2
        id="profile-facts-heading"
        className="font-heading text-lg font-medium"
      >
        Profile facts
      </h2>
      <p className="mt-1 text-sm text-muted-foreground">
        Facts the search brief carries for this profile. They are derived from
        the saved goals above, not verified CV facts.
      </p>
      <div className="mt-3">
        {context.briefStale ? (
          <Alert className="mb-3">
            <AlertTitle className="wrap-break-word">
              The brief is behind the saved profile
            </AlertTitle>
            <AlertDescription className="wrap-break-word">
              These facts belong to an older profile version. The next Find
              jobs run uses profile version {context.profileVersion}.
            </AlertDescription>
          </Alert>
        ) : null}
        {context.rubricVersion === null ? (
          <EmptyBlock
            title="No search brief yet"
            description="The first Find jobs run authors the brief from this profile."
          />
        ) : context.briefFacts.length === 0 ? (
          <EmptyBlock
            title="No brief facts recorded"
            description={`Brief ${context.rubricVersion} carries no facts for this profile.`}
          />
        ) : (
          <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 rounded-2xl border bg-card px-4 py-4 text-sm sm:grid-cols-2">
            {context.briefFacts.map((fact) => (
              <div key={fact.key} className="min-w-0">
                <dt className="text-muted-foreground wrap-break-word">
                  {fact.key}
                </dt>
                <dd className="wrap-break-word">{fact.value}</dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </section>
  )
}

export function SearchPage() {
  return (
    <SavedGoalsProvider>
      <SearchPageBody />
    </SavedGoalsProvider>
  )
}

function SearchPageBody() {
  const { session } = useSession()
  const [route] = useRoute()
  const owner = useOwnerContext()
  const savedGoals = useSavedGoals()
  const [draft, setDraft] = useState("")
  const draftRef = useRef<HTMLTextAreaElement>(null)
  const savedSeenRef = useRef<string | null>(null)
  const routeRunId = route.page === "search" ? route.runId : null

  // F2: the run deep link passes straight into discovery, which restores
  // it from the server GET-only. No browser pointer is written or read;
  // the route plus the server history are the recovery source (C1).
  const runHistory = useRead(
    "search:run-history",
    (signal) => listResearchRuns({ limit: 5 }, signal),
    { scopes: ["run"] }
  )
  const goalsSaved =
    savedGoals.state === "ready"
      ? (savedGoals.goals?.roleCriteria.length ?? 0) > 0
      : null
  const runState =
    runHistory.status === "ready"
      ? newestRunState(runHistory.data.items)
      : null
  const stageView = searchStageView({ goalsSaved, runState })

  const correction = owner.correction
  const correctionBusy =
    correction.kind === "sending" || correction.kind === "pending"

  const prefillCorrection = (anchor: string) => {
    setDraft((current) =>
      current === "" ? anchor : `${current.replace(/\s+$/, "")}\n${anchor}`
    )
    draftRef.current?.focus()
  }

  // A confirmed save consumes the draft; the saved status keeps the exact
  // wording that was stored. Accepted goal writes invalidate the shared
  // "goals" scope so Find-jobs readiness updates immediately (F1/C9).
  useEffect(() => {
    if (correction.kind !== "saved") return
    if (savedSeenRef.current === correction.requestKey) return
    savedSeenRef.current = correction.requestKey
    setDraft("")
    notifyGoalsAccepted()
  }, [correction])

  const handleGoalsSaved = () => {
    owner.retryContext()
    notifyGoalsAccepted()
  }

  const sendDisabled =
    correctionBusy ||
    owner.context === null ||
    draft.trim() === "" ||
    session === undefined ||
    session === null

  const sendUnavailableReason =
    owner.context === null
      ? "The saved profile is not loaded yet."
      : session === undefined
        ? "Checking dashboard session…"
        : session === null
          ? "Sign in to send a correction."
          : correctionBusy
            ? "A correction is already being saved."
            : draft.trim() === ""
              ? "Describe the change first."
              : null

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">
          Let&apos;s find your next role
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Set what you want once; the search, checks and drafts follow from
          those saved choices. Reading this page changes nothing.
        </p>
        <StageProgress
          ariaLabel="Seven-step journey"
          activeStageId={stageView.activeStageId}
          stages={stageView.stages}
          className="mt-3"
        />
      </div>

      {owner.contextState === "loading" ? (
        <LoadingBlock label="Loading search preferences…" />
      ) : owner.contextState === "error" || owner.context === null ? (
        <ErrorBlock
          title="Could not load search preferences"
          message={owner.contextError ?? "The request could not be completed."}
          onRetry={owner.retryContext}
        />
      ) : (
        <>
          <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
            <ExperiencePanel />
            <WantsPanel
              context={owner.context}
              csrfToken={session?.csrfToken ?? null}
              correctionBusy={correctionBusy}
              onCorrect={prefillCorrection}
              onSaved={handleGoalsSaved}
            />
          </div>
          <ProfileFactsPanel context={owner.context} />

          <section
            aria-labelledby="change-search-heading"
            className="rounded-2xl border bg-card px-4 py-4"
          >
            <h2
              id="change-search-heading"
              className="font-heading text-lg font-medium"
            >
              Change my search
            </h2>
            <p className="mt-1 text-sm text-muted-foreground">
              State the correction in your own words. It saves against profile
              version {owner.context.profileVersion}; nothing is sent to an
              employer.
            </p>
            <div className="mt-3 flex min-w-0 flex-col gap-2">
              <Label htmlFor="change-search-text">
                Describe the correction in your own words
              </Label>
              <Textarea
                id="change-search-text"
                ref={draftRef}
                rows={4}
                maxLength={20000}
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                placeholder='e.g. "I want remote-only roles in Berlin, at least 32 hours a week"'
                disabled={correctionBusy}
              />
            </div>
            <div className="mt-3 flex min-w-0 flex-wrap items-center gap-2">
              <Button
                type="button"
                disabled={sendDisabled}
                onClick={() => void owner.submitCorrection(draft)}
              >
                {correction.kind === "sending"
                  ? "Sending…"
                  : "Send correction"}
              </Button>
              {sendDisabled && sendUnavailableReason !== null ? (
                <p className="text-xs text-muted-foreground">
                  {sendUnavailableReason}
                </p>
              ) : null}
            </div>

            <div aria-live="polite" className="mt-3">
              {correction.kind === "sending" ? (
                <Alert role="status">
                  <AlertTitle className="wrap-break-word">
                    Sending your correction…
                  </AlertTitle>
                  <AlertDescription className="wrap-break-word">
                    Not accepted yet. Based on profile version{" "}
                    {correction.baseVersion}.
                  </AlertDescription>
                </Alert>
              ) : correction.kind === "pending" ? (
                <Alert role="status">
                  <AlertTitle className="wrap-break-word">
                    {correction.roundState === "paused"
                      ? "Paused — your accepted change is waiting"
                      : "Accepted — saving your change…"}
                  </AlertTitle>
                  <AlertDescription className="wrap-break-word">
                    Accepted, not yet saved. Round {correction.roundId} is{" "}
                    {correction.roundState}; based on profile version{" "}
                    {correction.baseVersion}. The new version appears here only
                    after the saved profile is read back.
                  </AlertDescription>
                  <AlertDescription className="wrap-break-word">
                    Your wording: “{correction.targetText}”
                  </AlertDescription>
                  <div className="col-start-2 mt-2">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => void owner.refreshCorrection()}
                    >
                      Check again
                    </Button>
                  </div>
                </Alert>
              ) : correction.kind === "saved" ? (
                <Alert role="status">
                  <AlertTitle className="wrap-break-word">
                    Saved as profile version {correction.savedVersion}.
                  </AlertTitle>
                  <AlertDescription className="wrap-break-word">
                    The saved profile was read back at version{" "}
                    {correction.savedVersion}; this is now the effective version
                    for the next Find jobs run. Your wording: “
                    {correction.targetText}”
                  </AlertDescription>
                  <div className="col-start-2 mt-2">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={owner.dismissCorrection}
                    >
                      Dismiss
                    </Button>
                  </div>
                </Alert>
              ) : correction.kind === "conflict" ? (
                <ErrorBlock
                  title="The profile changed before your correction was saved"
                  message={`${correction.message} Your change was based on version ${correction.baseVersion}; the saved profile is now version ${correction.currentVersion}.`}
                  onRetry={owner.dismissCorrection}
                  retryLabel="Dismiss"
                />
              ) : correction.kind === "error" ? (
                <ErrorBlock
                  title="Correction failed"
                  message={correction.message}
                  onRetry={
                    correction.retryable
                      ? () => void owner.submitCorrection(draft)
                      : owner.dismissCorrection
                  }
                  retryLabel={correction.retryable ? "Try again" : "Dismiss"}
                />
              ) : null}
            </div>
          </section>
        </>
      )}

      <DiscoverySection
        key={routeRunId ?? "live"}
        museScenario="live"
        runId={routeRunId}
      />
    </div>
  )
}
