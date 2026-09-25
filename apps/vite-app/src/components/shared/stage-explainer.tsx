// Stage explainers, ported from the connected-design prototype's
// check/answer/preparation explanations. One card per work stage tells the
// owner what happens, who does it, and what stays in their hands. Copy
// names the real actors: checks read saved evidence, Jev (a classifier,
// not an LLM) suggests answers, and preparation drafts from verified
// facts. Nothing here commissions work; all reads stay GET-only.
const COPY: Record<
  "check" | "answer" | "prepare",
  { title: string; body: string; note: string }
> = {
  check: {
    title: "Checking the jobs you chose",
    body: "For each chosen job, I'll read the full vacancy, check important gaps, and find the employer's actual questions, requested documents and application route. I won't invent questions.",
    note: "Already-saved findings are reused. You don't need to fill out the job details.",
  },
  answer: {
    title: "Answer the employer's questions",
    body: "Jev puts a fitting saved answer into an editable box. Change it, write your own, or leave it blank. Optional questions can stay blank.",
    note: "Answering uses no language model. In Prepare, blank required questions can be drafted from verified facts.",
  },
  prepare: {
    title: "Putting your application together",
    body: "I'll turn your answers into a clear application, tailor your CV, and write a short message if needed. If you left a required answer blank, it can be drafted from verified facts. Optional answers can stay blank.",
    note: "Nothing leaves this app. You copy the saved materials and apply manually from Handoff.",
  },
}

export function StageExplainer({ stage }: { stage: keyof typeof COPY }) {
  const copy = COPY[stage]
  return (
    <section
      aria-label={copy.title}
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <h2 className="font-heading text-lg font-medium">{copy.title}</h2>
      <p className="mt-2 text-sm wrap-break-word">{copy.body}</p>
      <p className="mt-2 text-xs wrap-break-word text-muted-foreground">
        {copy.note}
      </p>
    </section>
  )
}
