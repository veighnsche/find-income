import { listSavedAnswers } from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { useRead } from "@/pages/useRead"
import {
  getSourcedOwnerContext,
  isCareerSourceNotConnected,
} from "@/features/owner-context/sourced-context"

/**
 * Sourced experience panel (B2/F03, G1/D4). Leads with the approved career
 * sources (CV and supporting documents) and their provenance from the
 * GET-only sourced owner context: CV experience is visible even with zero
 * reusable answers. Approved reusable answers follow as a separate
 * section. Known facts never become wants; the panel writes nothing.
 */
export function ExperiencePanel() {
  // A missing endpoint is neutral absence ("not-connected"), not an error:
  // older servers predate D4 and the answers section below still applies.
  const sources = useRead("goals:sourced-context", (signal) =>
    getSourcedOwnerContext(signal).catch((cause: unknown) => {
      if (isCareerSourceNotConnected(cause)) return "not-connected" as const
      throw cause
    })
  )
  const answers = useRead("goals-experience", (signal) =>
    listSavedAnswers({ limit: 50 }, signal)
  )

  return (
    <section
      aria-labelledby="experience-heading"
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <h2 id="experience-heading" className="font-heading text-lg font-medium">
        Your experience
      </h2>
      <p className="mt-1 text-sm text-muted-foreground">
        Approved career sources and reusable facts. Drafts may reuse these;
        they never silently become search wants.
      </p>

      <div className="mt-3 flex min-w-0 flex-col gap-4">
        <div className="min-w-0">
          <h3 className="text-sm font-medium">Career sources</h3>
          <div className="mt-2">
            {sources.status === "loading" ? (
              <LoadingBlock label="Loading career sources…" />
            ) : sources.status === "error" || sources.data === null ? (
              <ErrorBlock
                title="Could not load career sources"
                message={sources.error ?? "The request could not be completed."}
                onRetry={sources.retry}
              />
            ) : sources.data === "not-connected" ? (
              <EmptyBlock
                title="Career sources are not available"
                description="This server does not expose approved career sources; reusable answers below still apply."
              />
            ) : sources.data.sources.length === 0 ? (
              <EmptyBlock
                title={
                  sources.data.sourcesConnected
                    ? "No career sources recorded"
                    : "Career sources are not connected"
                }
                description={
                  sources.data.sourcesConnected
                    ? `Signed in as ${sources.data.owner.kind} · ${sources.data.owner.id}. No approved CV or career documents are recorded yet.`
                    : `Signed in as ${sources.data.owner.kind} · ${sources.data.owner.id}. This server has no career-source loader connected; reusable answers below still apply.`
                }
              />
            ) : (
              <div className="flex min-w-0 flex-col gap-2">
                <p className="text-xs text-muted-foreground">
                  {`Signed in as ${sources.data.owner.kind} · ${sources.data.owner.id}.`}
                </p>
                <ul className="flex min-w-0 flex-col gap-2">
                  {sources.data.sources.map((source, index) => (
                    <li
                      key={source.id}
                      className="min-w-0 rounded-xl border px-3 py-2"
                    >
                      <details open={index === 0}>
                        <summary className="cursor-pointer text-sm font-medium wrap-break-word">
                          {source.name}
                        </summary>
                        <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
                          {`Source ${source.id} · sha ${source.sha256.slice(0, 12)}… · ${source.approved ? "approved" : "not approved"}`}
                        </p>
                        <p className="mt-2 text-sm wrap-break-word whitespace-pre-wrap">
                          {source.body}
                        </p>
                      </details>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </div>

        <div className="min-w-0">
          <h3 className="text-sm font-medium">Reusable answers</h3>
          <div className="mt-2">
            {answers.status === "loading" ? (
              <LoadingBlock label="Loading approved answers…" />
            ) : answers.status === "error" || answers.data === null ? (
              <ErrorBlock
                title="Could not load experience"
                message={answers.error ?? "The request could not be completed."}
                onRetry={answers.retry}
              />
            ) : answers.data.items.length === 0 ? (
              <EmptyBlock
                title="No approved answers yet"
                description="Owner-approved reusable answers will appear here with their scope tags."
              />
            ) : (
              <ul className="flex min-w-0 flex-col gap-3">
                {answers.data.items.map((answer) => {
                  const current = answer.versions.find(
                    (version) => version.version === answer.currentVersion
                  )
                  return (
                    <li
                      key={answer.id}
                      className="min-w-0 rounded-xl border px-3 py-2"
                    >
                      <p className="text-sm wrap-break-word">
                        {current?.text ?? "(no current text)"}
                      </p>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {answer.scopeTags.length > 0
                          ? `Scope: ${answer.scopeTags.join(", ")}`
                          : "Scope: none recorded"}
                        {answer.contextNote !== undefined &&
                        answer.contextNote !== ""
                          ? ` · ${answer.contextNote}`
                          : ""}
                        {` · v${answer.currentVersion}`}
                      </p>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>
        </div>
      </div>
    </section>
  )
}
