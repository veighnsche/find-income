import { useState } from "react"
import {
  commitRoleAnswers,
  isUnauthenticated,
  type RoleWorkflowState,
} from "@/api/client"
import { useSession } from "@/api/session"
import { notifyAccepted } from "@/components/shared/invalidation"
import { Button } from "@/components/ui/button"

// ZeroQuestionContinuation is the C1 explicit continuation for a verified
// questionless route (checked, zero employer questions, application_route).
// Mount, reads and reloads never commit: only the explicit button POSTs the
// K3 empty-set commit, then routes to the SAME job's Prepare task. Step 6
// starts solely from its own explicit Prepare action, never from this
// navigation. Roles already past answering get a plain navigation link so
// the commit is never pointlessly repeated; anything else stays held with
// an honest note instead of an action that could falsely advance.
export function ZeroQuestionContinuation({
  jobId,
  stage,
}: {
  jobId: string
  stage: RoleWorkflowState["stage"]
}) {
  const { session, loseSession } = useSession()
  const [committing, setCommitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const prepareHash = `#/jobs/${encodeURIComponent(jobId)}/prepare`

  if (
    stage === "answered" ||
    stage === "preparing" ||
    stage === "prepared"
  ) {
    return (
      <section
        aria-label="Continue to preparation"
        className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4"
      >
        <p className="text-sm wrap-break-word">
          This verified route has no employer questions and its answers are
          already committed. Preparation continues for this same job.
        </p>
        <p>
          <a
            href={prepareHash}
            className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            Open preparation
          </a>
        </p>
      </section>
    )
  }

  if (stage === "handoff_saved") {
    return (
      <section
        aria-label="Continue to preparation"
        className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4"
      >
        <p className="text-sm wrap-break-word">
          This verified route has no employer questions and the application
          already reached its saved handoff. Nothing further commits.
        </p>
        <p>
          <a
            href={`#/jobs/${encodeURIComponent(jobId)}/handoff`}
            className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            Open saved handoff
          </a>
        </p>
      </section>
    )
  }

  if (stage !== "checked" && stage !== "answering") {
    return (
      <section
        aria-label="Continue to preparation"
        className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4"
      >
        <p className="text-sm wrap-break-word" role="status">
          The saved application stage (“{stage}”) does not match this
          completed check, so continuation stays held. Refresh the saved
          check; if the mismatch persists, the role moved elsewhere.
        </p>
      </section>
    )
  }

  async function commitEmptySet() {
    if (session === undefined || session === null || committing) return
    setCommitting(true)
    setError(null)
    try {
      await commitRoleAnswers(jobId, session.csrfToken)
      notifyAccepted("answers", "workflows")
      window.location.hash = prepareHash
    } catch (cause: unknown) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(
        cause instanceof Error
          ? cause.message
          : "The request could not be completed."
      )
    } finally {
      setCommitting(false)
    }
  }

  return (
    <section
      aria-label="Continue to preparation"
      className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4"
    >
      <p className="text-sm wrap-break-word">
        This check verified the application route and recorded zero employer
        questions, so there is nothing to answer. Continuing commits the
        empty answer set and opens preparation for this same job.
        Preparation itself starts only from its own explicit action there.
      </p>
      <div>
        <Button
          type="button"
          disabled={committing || session === undefined || session === null}
          onClick={() => void commitEmptySet()}
        >
          {committing ? "Committing…" : "Continue to preparation"}
        </Button>
      </div>
      {session === null ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          Sign in to continue.
        </p>
      ) : null}
      {error === null ? null : (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {error}
        </p>
      )}
      <p className="text-xs wrap-break-word text-muted-foreground">
        Nothing leaves this app. You keep the saved check and apply manually
        from the Handoff page.
      </p>
    </section>
  )
}
