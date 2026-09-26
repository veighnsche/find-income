import {
  getRoleWorkflowOrNull,
  type RoleWorkflowState,
} from "@/api/client"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
} from "@/components/shared"
import { Button } from "@/components/ui/button"
import { stageStatusText } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

// Next-step copy per server stage. Labels name the destination stage page;
// sent and blocked are terminal here (the saved state or reason reads
// below), so they report status instead of linking anywhere.
function nextStep(workflow: RoleWorkflowState): {
  label: string
  description: string
  href: string | null
} {
  const id = encodeURIComponent(workflow.opportunityId)
  switch (workflow.stage) {
    case "selected":
      return {
        label: "Check job details",
        description: "Start the detail check for this role.",
        href: `#/jobs/${id}/check`,
      }
    case "checking":
      return {
        label: "Check job details",
        description: "A check is running. Follow its progress here.",
        href: `#/jobs/${id}/check`,
      }
    case "checked":
    case "answering":
      return {
        label: "Answer questions",
        description:
          "Review the employer questions and fill in your answer boxes.",
        href: `#/jobs/${id}/answers`,
      }
    case "answered":
    case "preparing":
      return {
        label: "Prepare materials",
        description: "Turn your answers into the application materials.",
        href: `#/jobs/${id}/prepare`,
      }
    case "prepared":
      return {
        label: "Prepare materials",
        description:
          "Review the prepared materials, then open the saved handoff below.",
        href: `#/jobs/${id}/prepare`,
      }
    case "reviewing":
      return {
        label: "Handoff",
        description:
          "Materials are ready. Open the saved handoff page for manual steps.",
        href: `#/jobs/${id}/handoff`,
      }
    case "sent":
      return {
        label: "Saved handoff",
        description:
          "This role reached the terminal saved state. Reopen its handoff any time.",
        href: `#/jobs/${id}/handoff`,
      }
    case "blocked":
      return {
        label: "Blocked",
        description:
          "This role has no next step until the block is resolved.",
        href: null,
      }
  }
}

// ApplicationContinue is the per-role next step on the application detail
// page: the prototype's staged "continue where you left off". It reads
// the server-owned workflow stage and links to that stage's page; other
// pages stay reachable, so stages are never compulsory visits.
export function ApplicationContinue({ jobId }: { jobId: string }) {
  const workflow = useRead(`application-continue:${jobId}`, (signal) =>
    getRoleWorkflowOrNull(jobId, signal)
  )

  return (
    <section aria-labelledby="application-continue-heading">
      <h2
        id="application-continue-heading"
        className="font-heading text-lg font-medium"
      >
        Continue this application
      </h2>
      <div className="mt-3">
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
            title="No application started"
            description="Choose this job to start its application work."
          />
        ) : (
          <ContinueBody workflow={workflow.data} />
        )}
      </div>
    </section>
  )
}

function ContinueBody({ workflow }: { workflow: RoleWorkflowState }) {
  const step = nextStep(workflow)
  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-xl border bg-card px-4 py-4">
      <p className="text-xs text-muted-foreground wrap-break-word">
        {stageStatusText(workflow)}
      </p>
      <p className="text-sm wrap-break-word">{step.description}</p>
      {step.href !== null ? (
        <div>
          <Button
            render={
              <a href={step.href}>{step.label}</a>
            }
          />
        </div>
      ) : (
        <p className="text-sm font-medium wrap-break-word">{step.label}</p>
      )}
    </div>
  )
}
