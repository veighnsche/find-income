import {
  SEVEN_STAGES,
  StageProgress,
} from "@/components/shared/stage-progress"
import { GroupedJobs } from "@/features/discovery/grouped-jobs"

export function JobsPage() {
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">Jobs</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Tracked roles grouped by their saved Jev recommendation. Each title
          links to its own page, which keeps working across refresh and
          browser back.
        </p>
        <StageProgress
          ariaLabel="Seven-step journey"
          activeStageId="select"
          stages={SEVEN_STAGES.map((stage, index) => ({
            ...stage,
            state: index < 2 ? ("complete" as const) : ("upcoming" as const),
          }))}
          className="mt-3"
        />
      </div>

      <GroupedJobs museScenario="live" />
    </div>
  )
}
