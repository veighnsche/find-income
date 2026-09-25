import { useCallback, useEffect, useRef, useState } from "react"
import type { CheckStatusView } from "@/api/client"
import { Button } from "@/components/ui/button"
import { useCheckStart } from "@/features/check"
import type { MuseReadiness } from "@/features/discovery/muse-state"

export interface ChosenRoleInput {
  jobId: string
  title: string
  opportunityRevision: number
  workflowRevision: number
}

function startedLabel(status: CheckStatusView["status"]): string {
  switch (status) {
    case "checking":
      return "Check pending"
    case "checked":
      return "Check complete"
    case "blocked":
      return "Check blocked"
    case "outdated":
      return "Saved check outdated"
    case "not_checked":
      return "Still not checked"
  }
}

function checkPageHref(jobId: string): string {
  return `#/jobs/${encodeURIComponent(jobId)}/check`
}

// One chosen role's check starter. It owns a D2 useCheckStart instance, so
// its request key, pending state, error and started outcome are independent
// of every other role. It starts only when startEpoch advances past the value
// observed at mount: mounting (selection) alone never starts a check, even
// when other roles already started in an earlier epoch.
function ChosenRoleCheckRow({
  role,
  startEpoch,
}: {
  role: ChosenRoleInput
  startEpoch: number
}) {
  const [outcome, setOutcome] = useState<CheckStatusView | null>(null)
  const handleStarted = useCallback((view: CheckStatusView) => {
    setOutcome(view)
  }, [])
  const action = useCheckStart({
    jobId: role.jobId,
    opportunityRevision: role.opportunityRevision,
    workflowRevision: role.workflowRevision,
    onStarted: handleStarted,
  })
  // Epoch observed at mount: a role selected after an earlier click must show
  // "Not started" rather than inherit the previous epoch's commanded state.
  const [mountEpoch] = useState(startEpoch)
  const lastEpochRef = useRef(mountEpoch)
  const { start } = action
  useEffect(() => {
    if (startEpoch > lastEpochRef.current) {
      lastEpochRef.current = startEpoch
      start()
    }
  }, [startEpoch, start])
  const commanded = startEpoch > mountEpoch

  const href = checkPageHref(role.jobId)
  return (
    <li className="flex min-w-0 flex-col gap-1 rounded-md border px-3 py-2">
      <p className="text-sm font-medium wrap-break-word">
        <a
          href={href}
          aria-label={`Open check page for ${role.title}`}
          className="underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          {role.title}
        </a>
      </p>
      {action.starting ? (
        <p className="text-sm text-muted-foreground">Starting check…</p>
      ) : action.error !== null ? (
        <>
          <p role="alert" className="text-sm wrap-break-word text-destructive">
            {action.error}
          </p>
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label={`Retry check for ${role.title}`}
              onClick={action.start}
            >
              Retry this role
            </Button>
          </div>
        </>
      ) : outcome !== null ? (
        <p className="text-sm wrap-break-word">
          {`${startedLabel(outcome.status)} — `}
          <a
            href={href}
            className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            Open check
          </a>
        </p>
      ) : commanded ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          {action.unavailableReason ??
            "Check was not started for this role."}
        </p>
      ) : (
        <p className="text-sm text-muted-foreground">
          Not started — selection alone starts nothing.
        </p>
      )}
    </li>
  )
}

// CheckChosenJobs is the C5 persistent Jobs action. It stays visible at the
// bottom of long lists (sticky) and is keyboard reachable through native
// buttons and links. An explicit click starts checks only for the chosen
// (server-selected) roles passed in; rendering or selecting roles starts
// nothing, and each role's blocked/pending outcome stays independent. When a
// Contributor readiness is supplied and not ready, the action stays disabled
// with the blocking code as the reason.
export function CheckChosenJobs({
  roles,
  contributor,
}: {
  roles: ChosenRoleInput[]
  contributor?: MuseReadiness | null
}) {
  const [startEpoch, setStartEpoch] = useState(0)
  const blocked =
    contributor !== undefined &&
    contributor !== null &&
    contributor.state !== "ready"

  return (
    <section
      aria-label="Check chosen jobs"
      className="sticky bottom-0 z-10 border-t border-border bg-background/95 py-3 backdrop-blur"
    >
      <div className="flex min-w-0 flex-col gap-2 rounded-lg border bg-card px-3 py-2.5">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <Button
            type="button"
            disabled={roles.length === 0 || blocked}
            onClick={() => setStartEpoch((value) => value + 1)}
          >
            {`Check chosen jobs (${roles.length})`}
          </Button>
        </div>
        <p className="text-xs wrap-break-word text-muted-foreground">
          Starts checks only for the chosen roles below — one independent
          request per role. Selecting a role starts nothing.
        </p>
        {blocked && contributor !== undefined && contributor !== null ? (
          <p className="text-xs wrap-break-word text-destructive" role="alert">
            {`Checks are blocked: Muse Contributor is ${contributor.state} (${contributor.code}) — ${contributor.detail}.`}
          </p>
        ) : null}
        {roles.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No chosen roles yet. Select a role above to enable checks.
          </p>
        ) : (
          <ul className="flex min-w-0 flex-col gap-2">
            {roles.map((role) => (
              <ChosenRoleCheckRow
                key={role.jobId}
                role={role}
                startEpoch={startEpoch}
              />
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
