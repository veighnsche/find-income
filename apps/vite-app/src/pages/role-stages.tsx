import {
  getRoleWorkflow,
  listOpportunities,
  listRoleWorkflows,
  RequestError,
  type OpportunityPage,
  type RoleWorkflowState,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import {
  SEVEN_STAGES,
  StageProgress,
  type StageInput,
} from "@/components/shared/stage-progress"
import { useRead, type ReadResult } from "@/pages/useRead"

/**
 * Role-stage → journey-stage mapping (B3, authoritative).
 *
 * The server owns one stage per chosen role (`RoleWorkflowState.stage`):
 * selected | checking | checked | answering | answered | preparing |
 * prepared | handoff_saved | blocked. The dashboard renders those nine
 * states on the fixed seven-step journey (Your goals → Find jobs →
 * Select jobs → Check job details → Answer questions → Prepare
 * materials → Handoff). Steps before the active step render as
 * complete, steps after it as upcoming; steps are progress markers, not
 * compulsory visits, so nothing here navigates or gates any section.
 *
 * | role stage | active journey step | completed before it          |
 * |------------|---------------------|-------------------------------|
 * | selected   | Select jobs       | Your goals, Find jobs       |
 * | checking   | Check job details | … + Select jobs             |
 * | checked    | Check job details | … + Select jobs             |
 * | answering  | Answer questions  | … + Check job details       |
 * | answered   | Answer questions  | … + Check job details       |
 * | preparing  | Prepare materials | … + Answer questions        |
 * | prepared   | Prepare materials | … + Answer questions        |
 * | handoff_saved | none (all seven complete) | all seven        |
 * | blocked    | none (unknown origin) | none marked            |
 *
 * Past-tense role stages (checked, answered, prepared) rest on their
 * journey step as current: the phase is recorded but the next phase has
 * not started, so claiming the next step would invent progress.
 * `handoff_saved` is the terminal saved state and completes the journey
 * visually. `blocked` carries a
 * free-text reason but no prior stage (the server reaches it from
 * checking or preparing), so the indicator marks nothing and the reason
 * is shown as text instead of guessing a step. Roles without a selected
 * owner decision answer 404 ("Role is not selected.") and render the
 * no-stage state, never an error. Stage transitions stay server-owned:
 * this module issues GET reads only and contains no transition writes.
 */

export type RoleStage = RoleWorkflowState["stage"]

const JOURNEY_INDEX: Record<string, number> = {
  goals: 0,
  find: 1,
  select: 2,
  check: 3,
  answer: 4,
  prepare: 5,
  handoff: 6,
}

const ROLE_TO_JOURNEY: Record<string, string> = {
  selected: "select",
  checking: "check",
  checked: "check",
  answering: "answer",
  answered: "answer",
  preparing: "prepare",
  prepared: "prepare",
}

export function journeyViewFor(stage: RoleStage): {
  stages: StageInput[]
  activeStageId: string | null
} {
  if (stage === "handoff_saved")
    return {
      stages: SEVEN_STAGES.map((item) => ({ ...item, state: "complete" })),
      activeStageId: null,
    }
  if (stage === "blocked")
    return {
      stages: SEVEN_STAGES.map((item) => ({ ...item, state: "upcoming" })),
      activeStageId: null,
    }
  const active = ROLE_TO_JOURNEY[stage] ?? "select"
  const activeIndex = JOURNEY_INDEX[active] ?? 2
  return {
    stages: SEVEN_STAGES.map((item, index) => ({
      ...item,
      state: index < activeIndex ? "complete" : "upcoming",
    })),
    activeStageId: active,
  }
}

export function compactStageLabel(stage: RoleStage): string {
  return stage.charAt(0).toUpperCase() + stage.slice(1)
}

export function journeyLabelFor(stage: RoleStage): string | null {
  if (stage === "handoff_saved" || stage === "blocked") return null
  const active = ROLE_TO_JOURNEY[stage] ?? "select"
  return SEVEN_STAGES.find((item) => item.id === active)?.label ?? null
}

export function stageStatusText(workflow: RoleWorkflowState): string {
  if (workflow.stage === "handoff_saved")
    return "Saved for handoff — all seven stages complete."
  if (workflow.stage === "blocked") {
    const reason = workflow.blockedReason ?? ""
    return reason === ""
      ? "Blocked — the server recorded no reason."
      : `Blocked — ${reason}`
  }
  const journey = journeyLabelFor(workflow.stage)
  return journey === null
    ? `Current stage: ${compactStageLabel(workflow.stage)}`
    : `Current stage: ${journey} (${workflow.stage})`
}

/**
 * 404-tolerant single-role read. The server answers 404 for unselected
 * (and missing) roles, which is the no-stage state — not a failure — so
 * it resolves to null exactly like the owner-decision read does.
 */
export function readRoleWorkflowOrNull(
  id: string,
  signal: AbortSignal
): Promise<RoleWorkflowState | null> {
  return getRoleWorkflow(id, signal).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404) return null
    throw cause
  })
}

export function RoleStageSection({ jobId }: { jobId: string }) {
  const workflow = useRead(
    `role-stage:${jobId}`,
    (signal) => readRoleWorkflowOrNull(jobId, signal),
    { scopes: ["workflows"] }
  )
  const roles = useRead(
    "role-stage:roles",
    (signal) => listRoleWorkflows(signal),
    { scopes: ["selection", "workflows"] }
  )
  const titles = useRead("role-stage:titles", (signal) =>
    listOpportunities("", signal)
  )

  return (
    <section
      aria-labelledby="job-stage-heading"
      className="flex min-w-0 flex-col gap-3"
    >
      <h2 id="job-stage-heading" className="font-heading text-lg font-medium">
        Application progress
      </h2>
      {workflow.status === "loading" ? (
        <LoadingBlock label="Loading application stage…" />
      ) : workflow.status === "error" ? (
        <ErrorBlock
          title="Could not load the application stage"
          message={workflow.error}
          onRetry={workflow.retry}
        />
      ) : workflow.data === null ? (
        <EmptyBlock
          title="No stage recorded"
          description="This role is not selected, so the server keeps no workflow stage for it. Only chosen roles show progress here."
        />
      ) : (
        <RoleStageIndicator workflow={workflow.data} />
      )}
      <ChosenRoleLinks
        currentId={jobId}
        roles={roles}
        titles={titles}
        workflowKnown={workflow.status === "ready"}
      />
    </section>
  )
}

export function RoleStageIndicator({
  workflow,
}: {
  workflow: RoleWorkflowState
}) {
  const view = journeyViewFor(workflow.stage)
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <StageProgress stages={view.stages} activeStageId={view.activeStageId} />
      <p className="text-sm wrap-break-word">{stageStatusText(workflow)}</p>
      <p className="text-xs text-muted-foreground">
        Stages show progress, not required visits — open any section in any
        order.
      </p>
    </div>
  )
}

function ChosenRoleLinks({
  currentId,
  roles,
  titles,
  workflowKnown,
}: {
  currentId: string
  roles: ReadResult<RoleWorkflowState[]>
  titles: ReadResult<OpportunityPage>
  workflowKnown: boolean
}) {
  if (!workflowKnown) return null
  if (roles.status === "loading" || titles.status === "loading")
    return <LoadingBlock label="Loading chosen roles…" />
  if (roles.status === "error" || titles.status === "error") {
    const message =
      roles.status === "error" ? roles.error : (titles.error ?? "")
    const retry = () => {
      roles.retry()
      titles.retry()
    }
    return (
      <ErrorBlock
        title="Could not load chosen roles"
        message={message}
        onRetry={retry}
      />
    )
  }
  const others = roles.data.filter(
    (entry) => entry.opportunityId !== currentId
  )
  if (others.length === 0) return null
  const titleById = new Map(
    titles.data.items.map((view) => [
      view.opportunity.id,
      view.opportunity.title,
    ])
  )
  return (
    <nav aria-label="Other chosen roles">
      <h3 className="font-heading text-sm font-medium">Other chosen roles</h3>
      <ul className="mt-2 flex min-w-0 flex-col gap-1.5">
        {others.map((entry) => {
          const raw = titleById.get(entry.opportunityId)
          const title =
            raw === undefined || raw === "" ? entry.opportunityId : raw
          return (
            <li key={entry.opportunityId} className="min-w-0 text-sm">
              <a
                href={`#/jobs/${encodeURIComponent(entry.opportunityId)}`}
                className="underline-offset-4 outline-none wrap-break-word hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
              >
                {title} · {compactStageLabel(entry.stage)}
              </a>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}
