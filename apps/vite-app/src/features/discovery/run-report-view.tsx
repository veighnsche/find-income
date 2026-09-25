import type { ResearchReportView } from "@/api/client"

function ReportList({
  id,
  items,
  empty,
}: {
  id: string
  items: string[]
  empty: string
}) {
  if (items.length === 0)
    return (
      <p className="mt-1 text-sm text-muted-foreground">{empty}</p>
    )
  return (
    <ul aria-labelledby={id} className="mt-1 flex min-w-0 flex-col gap-1">
      {items.map((item) => (
        <li
          key={item}
          className="text-sm wrap-break-word rounded-md border bg-card px-3 py-2"
        >
          {item}
        </li>
      ))}
    </ul>
  )
}

// RunReportView renders one getResearchReport response verbatim: saved
// outcomes, searched and reused lines, remaining uncertainty, the observed
// budget and suggested next work. Counts are list lengths; no percentages.
export function RunReportView({ report }: { report: ResearchReportView }) {
  const observed = report.budget.observed
  return (
    <section
      aria-label="Research report"
      className="flex min-w-0 flex-col gap-4 rounded-2xl border bg-card px-4 py-4"
    >
      <h3 className="font-heading text-base font-medium">Report</h3>

      <div className="min-w-0">
        <h4
          id="discovery-report-outcomes"
          className="text-sm font-medium"
        >
          Outcomes
        </h4>
        <ReportList
          id="discovery-report-outcomes"
          items={report.outcomes}
          empty="No outcomes recorded."
        />
      </div>

      <div className="min-w-0">
        <h4 id="discovery-report-searched" className="text-sm font-medium">
          Searched {report.searched.length}{" "}
          {report.searched.length === 1 ? "source" : "sources"}
        </h4>
        <ReportList
          id="discovery-report-searched"
          items={report.searched}
          empty="Nothing searched yet."
        />
      </div>

      <div className="min-w-0">
        <h4 id="discovery-report-reused" className="text-sm font-medium">
          Reused {report.reused.length}{" "}
          {report.reused.length === 1 ? "result" : "results"}
        </h4>
        <ReportList
          id="discovery-report-reused"
          items={report.reused}
          empty="No reused results."
        />
      </div>

      <div className="min-w-0">
        <h4
          id="discovery-report-uncertainty"
          className="text-sm font-medium"
        >
          Remaining uncertainty
        </h4>
        <ReportList
          id="discovery-report-uncertainty"
          items={report.uncertainty}
          empty="No uncertainty recorded."
        />
      </div>

      <div className="min-w-0">
        <h4 id="discovery-report-budget" className="text-sm font-medium">
          Budget
        </h4>
        <p className="mt-1 text-sm">
          Observed {observed.observed.actions} actions,{" "}
          {observed.observed.jev} Jev assessments, {observed.observed.turns}{" "}
          turns, {observed.observed.bytes} bytes.
          {report.budget.unknown ? " Unknown usage remains." : ""}
          {report.budget.stopReason !== undefined
            ? ` Stop reason: ${report.budget.stopReason}.`
            : ""}
        </p>
      </div>

      {report.nextWork !== undefined && report.nextWork.length > 0 ? (
        <div className="min-w-0">
          <h4
            id="discovery-report-next"
            className="text-sm font-medium"
          >
            Suggested next work
          </h4>
          <ReportList
            id="discovery-report-next"
            items={report.nextWork}
            empty="No suggested next work."
          />
        </div>
      ) : null}
    </section>
  )
}
