import {
  getCurrentOpportunityCheck,
  getOpportunity,
  getRoleWorkflowOrNull,
  type CheckStatusView,
  type RoleWorkflowState,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { StageExplainer } from "@/components/shared/stage-explainer"
import type { MuseReadiness } from "@/features/discovery/muse-state"
import { MuseReadinessPanel } from "@/features/discovery/muse-panels"
import { ArtifactsSection } from "@/features/prepare/ArtifactsSection"
import { RoleStageIndicator } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

// PreparePage is the D5 prepare-applications surface for one role, reached by
// deep link (#/jobs/:id/prepare, registered by the coordinator). Mount,
// reads and reloads are GET-only and commission nothing. Each mutation is
// an explicit per-role button with pins already observed from GET reads.
// All state is keyed by jobId and the detail section remounts per role, so
// one role's preparation never touches another's. An optional Standard
// readiness surfaces the drafting tier state.
export function PreparePage({
  jobId,
  standard,
}: {
  jobId: string
  standard?: MuseReadiness | null
}) {
  const opportunity = useRead(`prepare:${jobId}:opportunity`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const workflow = useRead(`prepare:${jobId}:workflow`, (signal) =>
    getRoleWorkflowOrNull(jobId, signal)
  )
  const title =
    opportunity.status === "ready" && opportunity.data.opportunity.title !== ""
      ? opportunity.data.opportunity.title
      : jobId

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p className="flex min-w-0 flex-wrap gap-x-4 gap-y-1">
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to job details
        </a>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to answers
        </a>
      </p>

      <div>
        <h1 className="font-heading text-2xl font-semibold wrap-break-word">
          Prepare materials
        </h1>
        <p className="mt-1 text-sm wrap-break-word text-muted-foreground">
          {title} · opening this page only reads saved state; drafting and
          editing each need an explicit click.
        </p>
      </div>

      <StageExplainer stage="prepare" />

      {standard !== undefined && standard !== null ? (
        <MuseReadinessPanel
          readiness={standard}
          heading="Muse Standard readiness"
        />
      ) : null}

      {opportunity.status === "loading" ? (
        <LoadingBlock label="Loading job details…" />
      ) : opportunity.status === "error" ? (
        <ErrorBlock
          title="Could not load this job"
          message={opportunity.error}
          onRetry={opportunity.retry}
        />
      ) : workflow.status === "loading" ? (
        <LoadingBlock label="Loading application stage…" />
      ) : workflow.status === "error" ? (
        <ErrorBlock
          title="Could not load the application stage"
          message={workflow.error}
          onRetry={workflow.retry}
        />
      ) : workflow.data === null ? (
        <EmptyBlock
          title="Role not selected"
          description="This role is not selected, so the server keeps no check or material state for it. Only chosen roles can be prepared."
        />
      ) : (
        <>
          <RoleStageIndicator workflow={workflow.data} />
          <PrepareDetailSection
            key={jobId}
            jobId={jobId}
            workflow={workflow.data}
          />
        </>
      )}
    </div>
  )
}

function PrepareDetailSection({
  jobId,
  workflow,
}: {
  jobId: string
  workflow: RoleWorkflowState
}) {
  const check = useRead(`prepare:${jobId}:check`, (signal) =>
    getCurrentOpportunityCheck(jobId, signal)
  )

  if (check.status === "loading") {
    return <LoadingBlock label="Loading check…" />
  }
  if (check.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the check"
        message={check.error}
        onRetry={check.retry}
      />
    )
  }
  return (
    <PrepareBody jobId={jobId} workflow={workflow} check={check.data} />
  )
}

function PrepareBody({
  jobId,
  workflow,
  check,
}: {
  jobId: string
  workflow: RoleWorkflowState
  check: CheckStatusView
}) {
  const detail = check.check ?? null
  if (check.status === "not_checked" || detail === null) {
    return (
      <EmptyBlock
        title="No check yet"
        description="Preparation starts from a completed job check. Start a check first; preparing unlocks once its questions are saved."
      />
    )
  }
  if (check.status === "checking") {
    return (
      <EmptyBlock
        title="Check in progress"
        description="The job check is still running. Preparation unlocks once the check completes."
      />
    )
  }
  if (check.status === "blocked") {
    return (
      <EmptyBlock
        title="Check blocked"
        description={
          detail.blockedReason !== undefined
            ? `The check could not complete (${detail.blockedReason.code}): ${detail.blockedReason.detail === "" ? "no detail recorded." : detail.blockedReason.detail} Resolve it with a recheck before preparing.`
            : "The check could not complete. Resolve it with a recheck before preparing."
        }
      />
    )
  }
  if (check.status === "outdated") {
    return (
      <EmptyBlock
        title="Check outdated"
        description="The role changed after this check completed. Start a recheck; preparation reopens on the fresh check."
      />
    )
  }

  return (
    <ArtifactsSection
      jobId={jobId}
      checkId={detail.id}
      questionSetSha256={detail.questionSetSha256}
      workflowRevision={workflow.revision}
    />
  )
}
