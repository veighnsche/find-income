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
      </div>

      <GroupedJobs />
    </div>
  )
}
