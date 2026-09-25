import { EmptyBlock } from "@/components/shared"
import {
  describeReadiness,
  type MuseReadiness,
  type MuseRunReport,
  type RunCheckpoint,
} from "@/features/discovery/muse-state"

// MuseReadinessPanel surfaces one tier's real readiness verbatim: state,
// frozen code and detail. Pure render over the view-model; no reads, no calls.
export function MuseReadinessPanel({
  readiness,
  heading = "Muse readiness",
}: {
  readiness: MuseReadiness
  heading?: string
}) {
  return (
    <section
      aria-label={heading}
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <h3 className="font-heading text-base font-medium">{heading}</h3>
      <p className="mt-1 text-sm wrap-break-word">
        {describeReadiness(readiness)}
      </p>
      {readiness.state === "ready" ? null : (
        <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
          Commissions for this tier stay disabled until readiness is ready.
        </p>
      )}
    </section>
  )
}

// MuseCheckpointsPanel lists durable run checkpoints: saved counts and refs.
// Empty means nothing saved yet — never a promise of hidden work.
export function MuseCheckpointsPanel({
  checkpoints,
}: {
  checkpoints: RunCheckpoint[]
}) {
  if (checkpoints.length === 0) {
    return (
      <EmptyBlock
        title="No Muse checkpoints saved yet"
        description="Checkpoints appear here as a run saves validated results."
      />
    )
  }
  const totalSaved = checkpoints.reduce(
    (total, point) => total + point.savedCount,
    0
  )
  return (
    <section
      aria-label="Muse run checkpoints"
      className="flex min-w-0 flex-col gap-2 rounded-2xl border bg-card px-4 py-4"
    >
      <h3 className="font-heading text-base font-medium">
        {`Run checkpoints (${checkpoints.length}) · ${totalSaved} saved`}
      </h3>
      <ol className="flex min-w-0 flex-col gap-2">
        {checkpoints.map((point, index) => (
          <li
            key={`${point.runRef}:${index}`}
            className="rounded-md border px-3 py-2 text-sm"
          >
            <p className="wrap-break-word font-medium">
              {`Checkpoint ${index + 1} · run ${point.runRef} · ${point.savedCount} saved`}
            </p>
            <p
              className="mt-0.5 text-xs text-muted-foreground wrap-break-word"
              title={point.updatedAt}
            >
              {point.lastSavedReceipt === ""
                ? "No saved receipt yet."
                : `Last saved receipt ${point.lastSavedReceipt}.`}
            </p>
          </li>
        ))}
      </ol>
    </section>
  )
}

// MuseReportPanel renders a partial or no-result run report honestly: real
// saved refs, searched/reused lines, gaps and the next action. Counts are
// list lengths; a no-result report says so plainly.
export function MuseReportPanel({ report }: { report: MuseRunReport }) {
  const noResult = report.savedRefs.length === 0
  return (
    <section
      aria-label="Muse run report"
      className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4"
    >
      <h3 className="font-heading text-base font-medium">
        {noResult ? "Run report — no suitable vacancy" : "Run report"}
      </h3>
      <p className="text-sm wrap-break-word">
        {`Run ${report.runRef} · ${report.tier} · ${report.outcome} — ${report.detail}.`}
      </p>
      <p className="text-sm wrap-break-word">
        {noResult
          ? "Saved roles: none."
          : `Saved roles (${report.savedRefs.length}): ${report.savedRefs.join(", ")}.`}
      </p>
      <p className="text-sm wrap-break-word">
        {report.searched.length === 0
          ? "Searched: nothing recorded."
          : `Searched (${report.searched.length}): ${report.searched.join(", ")}.`}
      </p>
      <p className="text-sm wrap-break-word">
        {report.reused.length === 0
          ? "Reused: no reused results."
          : `Reused (${report.reused.length}): ${report.reused.join(", ")}.`}
      </p>
      {report.gaps.length === 0 ? null : (
        <div className="min-w-0">
          <p className="text-sm font-medium">Gaps</p>
          <ul className="mt-1 flex min-w-0 flex-col gap-1">
            {report.gaps.map((gap) => (
              <li
                key={gap}
                className="text-sm wrap-break-word rounded-md border px-3 py-2"
              >
                {gap}
              </li>
            ))}
          </ul>
        </div>
      )}
      <p className="text-sm wrap-break-word font-medium">
        {`Next: ${report.nextAction}`}
      </p>
    </section>
  )
}
