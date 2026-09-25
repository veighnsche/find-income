import { getPreferences } from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { DiscoverySection } from "@/features/discovery/discovery-section"
import { formatCents } from "@/pages/format"
import { useRead } from "@/pages/useRead"

export function SearchPage() {
  const preferences = useRead("search:preferences", (signal) =>
    getPreferences(signal)
  )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">My search</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The saved search profile, exactly as the server returns it. Reading
          this page changes nothing.
        </p>
      </div>

      {preferences.status === "loading" ? (
        <LoadingBlock label="Loading search preferences…" />
      ) : preferences.status === "error" ? (
        <ErrorBlock
          title="Could not load search preferences"
          message={preferences.error}
          onRetry={preferences.retry}
        />
      ) : (
        <>
          <section
            aria-labelledby="search-profile-heading"
            className="rounded-2xl border bg-card px-4 py-4"
          >
            <h2
              id="search-profile-heading"
              className="font-heading text-lg font-medium"
            >
              Profile version {preferences.data.version}
            </h2>
            <dl className="mt-3 grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
              <div className="min-w-0">
                <dt className="text-muted-foreground">Preferred location</dt>
                <dd className="wrap-break-word">
                  {preferences.data.preferredLocation === ""
                    ? "Not recorded"
                    : preferences.data.preferredLocation}
                </dd>
              </div>
              <div className="min-w-0">
                <dt className="text-muted-foreground">Timezone</dt>
                <dd className="wrap-break-word">
                  {preferences.data.timezone === ""
                    ? "Not recorded"
                    : preferences.data.timezone}
                </dd>
              </div>
              <div className="min-w-0">
                <dt className="text-muted-foreground">Remote</dt>
                <dd>{preferences.data.allowRemote ? "Allowed" : "Not allowed"}</dd>
              </div>
              <div className="min-w-0">
                <dt className="text-muted-foreground">Hybrid</dt>
                <dd>{preferences.data.allowHybrid ? "Allowed" : "Not allowed"}</dd>
              </div>
              <div className="min-w-0">
                <dt className="text-muted-foreground">Target hours per week</dt>
                <dd>{preferences.data.targetHours}</dd>
              </div>
              <div className="min-w-0">
                <dt className="text-muted-foreground">Minimum monthly base</dt>
                <dd>
                  {formatCents(
                    preferences.data.minMonthlyBaseCents,
                    preferences.data.salaryCurrency
                  )}
                </dd>
              </div>
            </dl>
          </section>

          <section aria-labelledby="search-criteria-heading">
            <h2
              id="search-criteria-heading"
              className="font-heading text-lg font-medium"
            >
              Role criteria
            </h2>
            <div className="mt-3">
              {preferences.data.roleCriteria.length === 0 ? (
                <EmptyBlock
                  title="No role criteria saved"
                  description="The server returned an empty role-criteria list for this profile version."
                />
              ) : (
                <ol className="flex min-w-0 flex-col gap-2">
                  {preferences.data.roleCriteria.map((criterion) => (
                    <li
                      key={criterion.id}
                      className="rounded-md border bg-card px-3 py-2"
                    >
                      <p className="text-sm font-medium wrap-break-word">
                        {criterion.label}
                      </p>
                      <p className="mt-0.5 text-xs text-muted-foreground">
                        {criterion.kind} · {criterion.mode}
                      </p>
                      <p className="mt-1 text-sm wrap-break-word">
                        {criterion.description}
                      </p>
                    </li>
                  ))}
                </ol>
              )}
            </div>
          </section>
        </>
      )}

      <DiscoverySection />
    </div>
  )
}
