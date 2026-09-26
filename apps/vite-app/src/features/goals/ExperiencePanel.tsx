import { listSavedAnswers } from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { useRead } from "@/pages/useRead"

/**
 * Sourced experience panel (B2/F03). Lists the owner-approved reusable
 * answers with their scope tags: supported past facts the drafts may
 * reuse. It never writes preferences; nothing here becomes a want.
 */
export function ExperiencePanel() {
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
        Approved reusable facts with their scope. Drafts may reuse these;
        they never silently become search wants.
      </p>
      <div className="mt-3">
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
    </section>
  )
}
