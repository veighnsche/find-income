import type { ResearchActivityEvent } from "@/api/client"
import {
  ActivityDisclosure,
  type ActivityEntry,
} from "@/components/shared/activity-disclosure"
import { ErrorBlock } from "@/components/shared"
import { Button } from "@/components/ui/button"

// Server-defined journal kinds that need owner attention rather than
// describing forward progress. Anything unlisted keeps its default mapping.
const blockerEventKinds: ReadonlySet<string> = new Set([
  "exhausted",
  "lease_expired",
  "run.dispatch_fenced",
  "run.interrupted",
  "run.item_unmatched",
  "run.steer_rejected",
  "run.uncertain",
])

// Server-defined journal kinds that record a settled outcome.
const resultEventKinds: ReadonlySet<string> = new Set([
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

export function isJevEvent(event: ResearchActivityEvent): boolean {
  if (event.phase === "classify") return true
  return event.refs?.assessmentId !== undefined
}

export function isBlockerEvent(event: ResearchActivityEvent): boolean {
  return blockerEventKinds.has(event.kind)
}

function entryKindFor(event: ResearchActivityEvent): ActivityEntry["kind"] {
  if (isBlockerEvent(event)) return "blocker"
  if (event.refs?.captureId !== undefined) return "source"
  if (resultEventKinds.has(event.kind)) return "result"
  return "action"
}

export function toActivityEntry(
  event: ResearchActivityEvent,
  captureUrls: ReadonlyMap<string, string>
): ActivityEntry {
  const captureId = event.refs?.captureId
  const href =
    captureId !== undefined ? captureUrls.get(captureId) : undefined
  let text = event.summary
  const refs: string[] = []
  if (captureId !== undefined && href === undefined)
    refs.push(`capture ${captureId}`)
  if (event.refs?.assessmentId !== undefined)
    refs.push(`assessment ${event.refs.assessmentId}`)
  if (event.refs?.recordId !== undefined)
    refs.push(`record ${event.refs.recordId}`)
  if (refs.length > 0) text += ` (${refs.join(", ")})`
  return { id: event.eventId, kind: entryKindFor(event), text, href }
}

function describeFeedStatus(events: ResearchActivityEvent[]): string {
  const blockers = events.filter(isBlockerEvent).length
  const recorded = `${events.length} recorded`
  return blockers === 0 ? recorded : `${recorded} · ${blockers} blockers`
}

export interface RunActivityFeedProps {
  events: ResearchActivityEvent[]
  captureUrls: ReadonlyMap<string, string>
  nextCursor: string
  loadingMore: boolean
  loadMoreError: string | null
  onLoadMore: () => void
}

// RunActivityFeed renders journaled listResearchActivity entries only: real
// actions, results, source links and blockers, split into Codex collection
// and Jev classification. It fetches nothing and invents no activity.
export function RunActivityFeed({
  events,
  captureUrls,
  nextCursor,
  loadingMore,
  loadMoreError,
  onLoadMore,
}: RunActivityFeedProps) {
  const collectEvents = events.filter((event) => !isJevEvent(event))
  const classifyEvents = events.filter(isJevEvent)
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <ActivityDisclosure
        actor="codex"
        phase="Find jobs · collection"
        status={
          collectEvents.length === 0
            ? "No collection activity recorded"
            : describeFeedStatus(collectEvents)
        }
        entries={collectEvents.map((event) =>
          toActivityEntry(event, captureUrls)
        )}
        emptyText="No collection activity recorded yet."
      />
      <ActivityDisclosure
        actor="jev"
        phase="Find jobs · classification"
        status={
          classifyEvents.length === 0
            ? "No classification recorded"
            : describeFeedStatus(classifyEvents)
        }
        entries={classifyEvents.map((event) =>
          toActivityEntry(event, captureUrls)
        )}
        emptyText="No Jev classification activity recorded yet."
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
