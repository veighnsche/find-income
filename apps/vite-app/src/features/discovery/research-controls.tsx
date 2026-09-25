import type {
  ResearchAllowance,
  ResearchRunView,
  SteeringMessage,
} from "@/api/client"
import { ErrorBlock } from "@/components/shared"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

// Reviewed finite defaults for a commission (plan §8). Displayed, never
// silently changed; the supervisor owns the enforced values.
export const defaultResearchAllowance: ResearchAllowance = {
  timeMs: 900000,
  maxActions: 60,
  maxJev: 12,
  maxTurns: 8,
  maxConcurrent: 2,
}

const activeRunStates: ReadonlySet<ResearchRunView["state"]> = new Set([
  "queued",
  "running",
  "awaiting_input",
  "stopping",
])

export function isActiveRunState(state: ResearchRunView["state"]): boolean {
  return activeRunStates.has(state)
}

export interface RunCounts {
  saved: number
  unresolved: number
  investigations: number
  observedActions: number
  observedJev: number
  observedTurns: number
  observedBytes: number
  reservedActions: number
  reservedJev: number
  reservedTurns: number
  unknownUsage: boolean
}

// runCounts derives truthful counts from the persisted projection only.
export function runCounts(run: ResearchRunView): RunCounts {
  return {
    saved: run.savedIds.length,
    unresolved: run.unresolvedCount,
    investigations: run.investigations.length,
    observedActions: run.usage.observed.actions,
    observedJev: run.usage.observed.jev,
    observedTurns: run.usage.observed.turns,
    observedBytes: run.usage.observed.bytes,
    reservedActions: run.usage.reserved.actions,
    reservedJev: run.usage.reserved.jev,
    reservedTurns: run.usage.reserved.turns,
    unknownUsage: run.usage.unknown,
  }
}

export function describeRunState(state: ResearchRunView["state"]): string {
  switch (state) {
    case "queued":
      return "Queued — work has not started."
    case "running":
      return "Running — research is underway."
    case "awaiting_input":
      return "Awaiting input — the run needs an owner decision."
    case "stopping":
      return "Stopping — new work is fenced while execution settles."
    case "paused":
      return "Paused — resume continues with the remaining allowance."
    case "completed":
      return "Completed — outcomes and report are saved."
    case "failed":
      return "Failed — see the report for what is known."
  }
}

export function newIdempotencyKey(): string {
  const cryptoRef =
    typeof globalThis.crypto === "object" &&
    globalThis.crypto !== null &&
    "randomUUID" in globalThis.crypto
      ? (globalThis.crypto as { randomUUID?: () => string })
      : null
  if (cryptoRef?.randomUUID !== undefined) {
    try {
      return cryptoRef.randomUUID()
    } catch {
      // Fall through to the insecure generator below.
    }
  }
  return `run-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

export interface ResearchControlsProps {
  run: ResearchRunView | null
  busy: boolean
  actionError: string | null
  briefText: string
  onBriefTextChange: (value: string) => void
  steerText: string
  onSteerTextChange: (value: string) => void
  steerAck: SteeringMessage | null
  onStart: () => void
  onStop: () => void
  onResume: () => void
  onRefresh: () => void
  onSteer: () => void
  onFindMore: () => void
}

// ResearchControls renders explicit run actions only. It never commissions,
// stops, resumes or steers during render; every mutation runs from an owner
// click handler owned by the parent section.
export function ResearchControls({
  run,
  busy,
  actionError,
  briefText,
  onBriefTextChange,
  steerText,
  onSteerTextChange,
  steerAck,
  onStart,
  onStop,
  onResume,
  onRefresh,
  onSteer,
  onFindMore,
}: ResearchControlsProps) {
  if (run === null) {
    return (
      <div className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4">
        <h3 className="font-heading text-base font-medium">
          Start a research run
        </h3>
        <p className="text-sm text-muted-foreground">
          Codex chooses sources and queries from the current search brief, then
          Jev classifies what was collected. The allowance is finite: 15
          minutes, 60 actions, 12 Jev assessments, 8 turns, at most 2 concurrent
          operations. Nothing starts until the button below is pressed.
        </p>
        <div className="flex min-w-0 flex-col gap-2">
          <Label htmlFor="discovery-brief">
            Recruitment intent (optional)
          </Label>
          <Textarea
            id="discovery-brief"
            rows={3}
            maxLength={20000}
            value={briefText}
            onChange={(event) => onBriefTextChange(event.target.value)}
            placeholder="What should the agent look for?"
            disabled={busy}
          />
        </div>
        <div>
          <Button type="button" disabled={busy} onClick={onStart}>
            {busy ? "Starting…" : "Start research run"}
          </Button>
        </div>
        {actionError !== null ? (
          <ErrorBlock
            title="Research action failed"
            message={actionError}
          />
        ) : null}
      </div>
    )
  }

  const counts = runCounts(run)
  return (
    <div className="flex min-w-0 flex-col gap-4 rounded-2xl border bg-card px-4 py-4">
      <div className="min-w-0">
        <h3 className="font-heading text-base font-medium">Research run</h3>
        <p className="mt-1 text-sm">{describeRunState(run.state)}</p>
        <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
          Brief v{run.briefVersion.profileVersion} ({run.briefVersion.rubricVersion}
          ){run.stopReason !== undefined ? ` — stopped: ${run.stopReason}` : ""}
        </p>
      </div>

      <ul className="flex min-w-0 flex-col gap-1 text-sm">
        <li>Saved records: {counts.saved}</li>
        <li>Unresolved findings: {counts.unresolved}</li>
        <li>Investigations: {counts.investigations}</li>
        <li>
          Actions observed {counts.observedActions}, reserved{" "}
          {counts.reservedActions}
          {counts.unknownUsage ? ", plus unknown usage" : ""}
        </li>
        <li>
          Jev assessments observed {counts.observedJev}, turns observed{" "}
          {counts.observedTurns}
        </li>
      </ul>

      <div className="flex min-w-0 flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={onStop}
        >
          Stop
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={onResume}
        >
          Resume
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={onRefresh}
        >
          Refresh
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={onFindMore}
        >
          Find more jobs
        </Button>
      </div>

      <div className="flex min-w-0 flex-col gap-2">
        <Label htmlFor="discovery-steer">Steer the run</Label>
        <Textarea
          id="discovery-steer"
          rows={2}
          maxLength={20000}
          value={steerText}
          onChange={(event) => onSteerTextChange(event.target.value)}
          placeholder="Message or correction for the running agent"
          disabled={busy}
        />
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={busy || steerText.trim() === ""}
            onClick={onSteer}
          >
            Send steering message
          </Button>
        </div>
        {steerAck !== null ? (
          <p className="text-xs text-muted-foreground">
            Message {steerAck.ack}.
          </p>
        ) : null}
      </div>

      {actionError !== null ? (
        <ErrorBlock title="Research action failed" message={actionError} />
      ) : null}
    </div>
  )
}
