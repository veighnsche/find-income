import type { ResearchActivityEvent } from "@/api/client"
import {
  ActivityDisclosure,
  type ActivityEntry,
} from "@/components/shared/activity-disclosure"
import { ErrorBlock } from "@/components/shared"
import { Button } from "@/components/ui/button"

// Server-defined journal kinds that need owner attention rather than
// describing forward progress. Anything unlisted keeps its default mapping.
const checkBlockerKinds: ReadonlySet<string> = new Set([
  "exhausted",
  "lease_expired",
  "run.dispatch_fenced",
  "run.interrupted",
  "run.item_unmatched",
  "run.steer_rejected",
  "run.uncertain",
])

// Server-defined journal kinds that record a settled outcome.
const checkResultKinds: ReadonlySet<string> = new Set([
  "capture",
  "late_observation",
  "note",
  "observation",
  "reuse",
  "run.checkpointed",
  "run.correction_applied",
  "run.item_completed",
  "run.observed",
  "run.recovered",
  "run.resumed",
  "run.saved",
  "run.steered",
  "run.stopped",
  "run.turn_observed",
  "run.unknown_events",
])

export function isCheckBlockerEvent(event: ResearchActivityEvent): boolean {
  return checkBlockerKinds.has(event.kind)
}

export function checkEntryKindFor(
  event: ResearchActivityEvent
): ActivityEntry["kind"] {
  if (isCheckBlockerEvent(event)) return "blocker"
  if (event.refs?.captureId !== undefined) return "source"
  if (checkResultKinds.has(event.kind)) return "result"
  return "action"
}

// Check-scope entry mapping. Capture/assessment/record ids stay visible as
// bare ids: the check page resolves no capture URLs, so a link would invent
// a destination the read did not provide.
export function toCheckActivityEntry(
  event: ResearchActivityEvent
): ActivityEntry {
  let text = event.summary
  const refs: string[] = []
  if (event.refs?.captureId !== undefined)
    refs.push(`capture ${event.refs.captureId}`)
  if (event.refs?.assessmentId !== undefined)
    refs.push(`assessment ${event.refs.assessmentId}`)
  if (event.refs?.recordId !== undefined)
    refs.push(`record ${event.refs.recordId}`)
  if (refs.length > 0) text += ` (${refs.join(", ")})`
  return { id: event.eventId, kind: checkEntryKindFor(event), text }
}

function describeCheckFeedStatus(events: ResearchActivityEvent[]): string {
  const blockers = events.filter(isCheckBlockerEvent).length
  const recorded = `${events.length} recorded`
  return blockers === 0 ? recorded : `${recorded} · ${blockers} blockers`
}

export interface CheckActivityFeedProps {
  events: ResearchActivityEvent[]
  nextCursor: string
  loadingMore: boolean
  loadMoreError: string | null
  onLoadMore: () => void
}

// CheckActivityFeed renders journaled per-role check activity only: real
// actions, results, source refs and blockers. It fetches nothing and invents
// no activity; pagination stays with the owning page.
export function CheckActivityFeed({
  events,
  nextCursor,
  loadingMore,
  loadMoreError,
  onLoadMore,
}: CheckActivityFeedProps) {
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <ActivityDisclosure
        actor="codex"
        phase="Check job details · activity"
        status={
          events.length === 0
            ? "No check activity recorded"
            : describeCheckFeedStatus(events)
        }
        entries={events.map(toCheckActivityEntry)}
        emptyText="No check activity recorded yet."
      />
      {nextCursor !== "" ? (
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={loadingMore}
            onClick={onLoadMore}
          >
            {loadingMore ? "Loading…" : "Load more activity"}
          </Button>
        </div>
      ) : null}
      {loadMoreError !== null ? (
        <ErrorBlock
          title="Could not load more activity"
          message={loadMoreError}
          onRetry={onLoadMore}
        />
      ) : null}
    </div>
  )
}
