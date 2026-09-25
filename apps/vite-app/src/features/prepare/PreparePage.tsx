import {
  getApplicationPack,
  getCurrentOpportunityCheck,
  getCurrentOpportunityMaterials,
  getOpportunity,
  getRoleWorkflowOrNull,
  type ApplicationPackDetail,
  type CheckStatusView,
  type MaterialStatusView,
  type MaterialVersion,
  type RoleWorkflowState,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { MuseReadiness } from "@/features/discovery/muse-state"
import { MuseReadinessPanel } from "@/features/discovery/muse-panels"
import {
  REWRITE_INSTRUCTION_RUNE_LIMIT,
  countBytes,
  countRunes,
  useMaterialEdit,
  useMaterialRewrite,
  usePrepareStart,
} from "@/features/prepare/usePrepareActions"
import { formatDate } from "@/pages/format"
import { RoleStageIndicator } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

type CheckDetail = NonNullable<CheckStatusView["check"]>
type CheckQuestion = CheckDetail["questions"][number]

// PreparePage is the D5 prepare-applications surface for one role, reached by
// deep link (#/jobs/:id/prepare, registered by the coordinator). Mount,
// reads and reloads are GET-only and commission nothing: no prepare, no
// edit, no rewrite. Each mutation is an explicit per-role button with pins
// already observed from GET reads. All state is keyed by jobId and the
// detail section remounts per role, so one role's preparation never touches
// another's. An optional Standard readiness surfaces the drafting tier state;
// a blocked tier is shown plainly while exact owner edits stay available.
export function PreparePage({
  jobId,
  standard,
}: {
  jobId: string
  standard?: MuseReadiness | null
}) {
  const opportunity = useRead(`prepare:${jobId}:opportunity`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const workflow = useRead(`prepare:${jobId}:workflow`, (signal) =>
    getRoleWorkflowOrNull(jobId, signal)
  )
  const title =
    opportunity.status === "ready" && opportunity.data.opportunity.title !== ""
      ? opportunity.data.opportunity.title
      : jobId

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p className="flex min-w-0 flex-wrap gap-x-4 gap-y-1">
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to job details
        </a>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to answers
        </a>
      </p>

      <div>
        <h1 className="font-heading text-2xl font-semibold wrap-break-word">
          Prepare applications
        </h1>
        <p className="mt-1 text-sm wrap-break-word text-muted-foreground">
          {title} · opening this page only reads saved state; preparing,
          editing and rewriting each need an explicit click.
        </p>
      </div>

      {standard !== undefined && standard !== null ? (
        <MuseReadinessPanel
          readiness={standard}
          heading="Muse Standard readiness"
        />
      ) : null}

      {opportunity.status === "loading" ? (
        <LoadingBlock label="Loading job details…" />
      ) : opportunity.status === "error" ? (
        <ErrorBlock
          title="Could not load this job"
          message={opportunity.error}
          onRetry={opportunity.retry}
        />
      ) : workflow.status === "loading" ? (
        <LoadingBlock label="Loading application stage…" />
      ) : workflow.status === "error" ? (
        <ErrorBlock
          title="Could not load the application stage"
          message={workflow.error}
          onRetry={workflow.retry}
        />
      ) : workflow.data === null ? (
        <EmptyBlock
          title="Role not selected"
          description="This role is not selected, so the server keeps no check or material state for it. Only chosen roles can be prepared."
        />
      ) : (
        <>
          <RoleStageIndicator workflow={workflow.data} />
          <PrepareDetailSection
            key={jobId}
            jobId={jobId}
            workflow={workflow.data}
          />
        </>
      )}
    </div>
  )
}

function PrepareDetailSection({
  jobId,
  workflow,
}: {
  jobId: string
  workflow: RoleWorkflowState
}) {
  const check = useRead(`prepare:${jobId}:check`, (signal) =>
    getCurrentOpportunityCheck(jobId, signal)
  )
  const materials = useRead(`prepare:${jobId}:materials`, (signal) =>
    getCurrentOpportunityMaterials(jobId, signal)
  )

  if (check.status === "loading" || materials.status === "loading") {
    return <LoadingBlock label="Loading check and materials…" />
  }
  if (check.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the check"
        message={check.error}
        onRetry={check.retry}
      />
    )
  }
  if (materials.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the materials"
        message={materials.error}
        onRetry={materials.retry}
      />
    )
  }
  return (
    <PrepareBody
      jobId={jobId}
      workflow={workflow}
      check={check.data}
      materials={materials.data}
      onMaterialsChanged={materials.retry}
    />
  )
}

function PrepareBody({
  jobId,
  workflow,
  check,
  materials,
  onMaterialsChanged,
}: {
  jobId: string
  workflow: RoleWorkflowState
  check: CheckStatusView
  materials: MaterialStatusView
  onMaterialsChanged: () => void
}) {
  const detail = check.check ?? null
  if (check.status === "not_checked" || detail === null) {
    return (
      <EmptyBlock
        title="No check yet"
        description="Preparation starts from a completed job check. Start a check first; preparing unlocks once its questions are saved."
      />
    )
  }
  if (check.status === "checking") {
    return (
      <EmptyBlock
        title="Check in progress"
        description="The job check is still running. Preparation unlocks once the check completes."
      />
    )
  }
  if (check.status === "blocked") {
    return (
      <EmptyBlock
        title="Check blocked"
        description={
          detail.blockedReason !== undefined
            ? `The check could not complete (${detail.blockedReason.code}): ${detail.blockedReason.detail === "" ? "no detail recorded." : detail.blockedReason.detail} Resolve it with a recheck before preparing.`
            : "The check could not complete. Resolve it with a recheck before preparing."
        }
      />
    )
  }
  if (check.status === "outdated") {
    return (
      <EmptyBlock
        title="Check outdated"
        description="The role changed after this check completed. Start a recheck; preparation reopens on the fresh check."
      />
    )
  }

  const current = materials.current ?? null
  if (materials.status === "not_prepared" || current === null) {
    if (materials.status !== "not_prepared") {
      return (
        <ErrorBlock
          title="Materials unavailable"
          message={`The server reported status "${materials.status}" without a current version. Reload the page; if this persists, the saved state needs attention.`}
          onRetry={onMaterialsChanged}
        />
      )
    }
    return (
      <PreparePrompt
        jobId={jobId}
        detail={detail}
        workflow={workflow}
        mode="prepare"
        onDone={onMaterialsChanged}
      />
    )
  }
  if (materials.status === "preparing") {
    return (
      <div className="flex min-w-0 flex-col gap-3">
        <EmptyBlock
          title="Preparation running"
          description="A preparation run is in progress for this role. Reload to pick up the new version once it completes; this page starts no additional work."
        />
        <p>
          <Button type="button" size="sm" onClick={onMaterialsChanged}>
            Reload materials
          </Button>
        </p>
        <MaterialVersionPanel
          jobId={jobId}
          detail={detail}
          current={current}
          stale
          onChanged={onMaterialsChanged}
        />
      </div>
    )
  }
  if (materials.status === "outdated") {
    return (
      <div className="flex min-w-0 flex-col gap-6">
        <PreparePrompt
          jobId={jobId}
          detail={detail}
          workflow={workflow}
          mode="reprepare"
          onDone={onMaterialsChanged}
        />
        <MaterialVersionPanel
          jobId={jobId}
          detail={detail}
          current={current}
          stale
          onChanged={onMaterialsChanged}
        />
      </div>
    )
  }
  return (
    <MaterialVersionPanel
      jobId={jobId}
      detail={detail}
      current={current}
      stale={false}
      onChanged={onMaterialsChanged}
    />
  )
}

function PreparePrompt({
  jobId,
  detail,
  workflow,
  mode,
  onDone,
}: {
  jobId: string
  detail: CheckDetail
  workflow: RoleWorkflowState
  mode: "prepare" | "reprepare"
  onDone: () => void
}) {
  const prepare = usePrepareStart({
    jobId,
    checkId: detail.id,
    questionSetSha256: detail.questionSetSha256,
    workflowRevision: workflow.revision,
    onDone,
  })
  return (
    <section
      aria-label={mode === "prepare" ? "Prepare materials" : "Re-prepare materials"}
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        {mode === "prepare"
          ? "No prepared application yet"
          : "Materials are outdated"}
      </h2>
      <p className="text-sm wrap-break-word text-muted-foreground">
        {mode === "prepare"
          ? `Assemble the first version from the completed check (${detail.questions.length} ${detail.questions.length === 1 ? "question" : "questions"}) and exact saved answers. Required items with no known fact stay visibly held; nothing is sent.`
          : "The check, answers, or role changed after this version was prepared. Re-preparing assembles a fresh version from the current saved state; prior versions are kept."}
      </p>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={!prepare.canStart}
          onClick={prepare.start}
        >
          {prepare.starting
            ? "Preparing…"
            : mode === "prepare"
              ? "Prepare application"
              : "Re-prepare application"}
        </Button>
        {prepare.unavailableReason !== null ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            {prepare.unavailableReason}
          </p>
        ) : null}
      </div>
      {prepare.error !== null ? (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {prepare.error}
        </p>
      ) : null}
    </section>
  )
}

function originLabel(origin: MaterialVersion["provenance"]["origin"]): string {
  switch (origin) {
    case "prepared":
      return "Prepared from verified facts"
    case "direct_edit":
      return "Direct owner edit (no model)"
    case "rewrite":
      return "Explicit rewrite"
  }
}

function requiredLabel(required: CheckQuestion["required"]): string {
  switch (required) {
    case "required":
      return "Required"
    case "optional":
      return "Optional"
    default:
      return "Requiredness unknown"
  }
}

function shortSha(sha: string): string {
  return sha.length > 12 ? `${sha.slice(0, 12)}…` : sha
}

function MaterialVersionPanel({
  jobId,
  detail,
  current,
  stale,
  onChanged,
}: {
  jobId: string
  detail: CheckDetail
  current: MaterialVersion
  stale: boolean
  onChanged: () => void
}) {
  const held = !current.readiness.ready
  const eligible = !stale && current.readiness.ready
  const questionsById = new Map(detail.questions.map((q) => [q.id, q]))
  const missing = current.readiness.missingRequired.map(
    (id) => questionsById.get(id)?.text ?? id
  )
  const heldItems = current.readiness.held.map(
    (id) => questionsById.get(id)?.text ?? id
  )
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <section
        aria-label={`Material version ${current.version}`}
        className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
      >
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h2 className="min-w-0 flex-1 text-base font-semibold wrap-break-word">
            Version {current.version}
          </h2>
          <Badge variant={held ? "secondary" : "default"}>
            {stale ? "Outdated" : held ? "Held" : "Ready"}
          </Badge>
          <Badge variant="secondary">{originLabel(current.provenance.origin)}</Badge>
        </div>
        {stale ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            This version no longer matches the saved check, answers, or role.
            Re-prepare above for a fresh version; edits and rewrites stay
            available on the stale base but will conflict once state moves on.
          </p>
        ) : null}
        {held && !stale ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Held: {missing.length}{" "}
            {missing.length === 1 ? "required item" : "required items"} still{" "}
            {missing.length === 1 ? "needs" : "need"} an owner-known fact.
            Unanswered required items stay blank rather than invented.
          </p>
        ) : null}
        <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 text-sm wrap-break-word sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">Origin</dt>
            <dd>
              {originLabel(current.provenance.origin)}
              {current.provenance.origin === "rewrite" &&
              current.provenance.rewriteOf !== undefined
                ? ` of v${current.provenance.rewriteOf}`
                : null}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Created</dt>
            <dd>
              {formatDate(current.createdAt)} by {current.createdBy.actorId} (
              {current.createdBy.actorKind})
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Check</dt>
            <dd>
              {current.checkId} · set {shortSha(current.questionSetSha256)}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Revisions</dt>
            <dd>
              role r{current.opportunityRevision} · profile r
              {current.profileRevision} · pack {current.packId}
            </dd>
          </div>
        </dl>
        <div>
          <h3 className="text-sm font-medium wrap-break-word">Source SHAs</h3>
          {current.provenance.sourceShas.length === 0 ? (
            <p className="mt-1 text-sm wrap-break-word text-muted-foreground">
              No source SHAs recorded.
            </p>
          ) : (
            <ul className="mt-1 flex min-w-0 flex-col gap-1 text-xs wrap-break-word text-muted-foreground">
              {current.provenance.sourceShas.map((sha) => (
                <li key={sha} className="font-mono wrap-break-word">
                  {sha}
                </li>
              ))}
            </ul>
          )}
        </div>
        {missing.length > 0 || heldItems.length > 0 ? (
          <div className="flex min-w-0 flex-col gap-2">
            <h3 className="text-sm font-medium wrap-break-word">
              Unanswered required items
            </h3>
            <ul className="flex min-w-0 flex-col gap-1 text-sm wrap-break-word">
              {missing.map((text) => (
                <li key={`missing:${text}`}>Missing required: {text}</li>
              ))}
              {heldItems
                .filter((text) => !missing.includes(text))
                .map((text) => (
                  <li key={`held:${text}`}>Held: {text}</li>
                ))}
            </ul>
          </div>
        ) : null}
      </section>

      <QuestionRefs detail={detail} current={current} />
      <PackPanel jobId={jobId} current={current} />
      <DirectEditSection
        key={`edit:${current.version}`}
        jobId={jobId}
        current={current}
        onSaved={onChanged}
      />
      <RewriteSection
        key={`rewrite:${current.version}`}
        jobId={jobId}
        current={current}
        onRewritten={onChanged}
      />
      <AdvanceSection jobId={jobId} current={current} eligible={eligible} />
    </div>
  )
}

function QuestionRefs({
  detail,
  current,
}: {
  detail: CheckDetail
  current: MaterialVersion
}) {
  const questionsById = new Map(detail.questions.map((q) => [q.id, q]))
  const missing = new Set(current.readiness.missingRequired)
  const held = new Set(current.readiness.held)
  return (
    <section
      aria-label="Per-question references"
      className="flex min-w-0 flex-col gap-3"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Per-question references
      </h2>
      {current.answers.length === 0 ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          This version references no questions.
        </p>
      ) : (
        <ul className="flex min-w-0 flex-col gap-3">
          {current.answers.map((ref, index) => {
            const question = questionsById.get(ref.questionId)
            const isMissing = missing.has(ref.questionId)
            const isHeld = held.has(ref.questionId)
            return (
              <li
                key={ref.questionId}
                className="flex min-w-0 flex-col gap-1 rounded-xl border border-border p-4"
              >
                <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
                  <p className="min-w-0 flex-1 text-sm font-medium wrap-break-word">
                    {index + 1}. {question?.text ?? ref.questionId}
                  </p>
                  <span className="flex flex-wrap gap-2">
                    {question !== undefined ? (
                      <Badge
                        variant={
                          question.required === "required"
                            ? "default"
                            : "secondary"
                        }
                      >
                        {requiredLabel(question.required)}
                      </Badge>
                    ) : null}
                    {isMissing ? <Badge variant="default">Missing</Badge> : null}
                    {isHeld && !isMissing ? (
                      <Badge variant="secondary">Held</Badge>
                    ) : null}
                  </span>
                </div>
                <p className="text-xs wrap-break-word text-muted-foreground">
                  {ref.answerVersion === 0
                    ? "Drafted into this version (no saved answer row)"
                    : `Saved answer v${ref.answerVersion}`}{" "}
                  · text {shortSha(ref.textSha256)}
                </p>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}

// Deterministic rendering of the pack text shown below it: focus, cover
// lines, then each answered question with its lines. The direct-edit box can
// adopt this rendering as a starting point; the server still stores whatever
// the owner saves, byte-exact.
export function renderPackText(detail: ApplicationPackDetail): string {
  const parts: string[] = [detail.manifest.draft.focus.text]
  for (const line of detail.manifest.draft.cover) parts.push(line.text)
  for (const answer of detail.manifest.draft.answers) {
    parts.push(answer.question)
    for (const line of answer.lines) parts.push(line.text)
  }
  return parts.filter((part) => part !== "").join("\n\n")
}

function PackPanel({
  jobId,
  current,
}: {
  jobId: string
  current: MaterialVersion
}) {
  const pack = useRead(
    `prepare:${jobId}:pack:${current.packId}:v${current.version}`,
    (signal) => getApplicationPack(current.packId, signal)
  )
  return (
    <section
      aria-label="Prepared materials"
      className="flex min-w-0 flex-col gap-3"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Prepared materials
      </h2>
      {pack.status === "loading" ? (
        <LoadingBlock label="Loading prepared materials…" />
      ) : pack.status === "error" ? (
        <ErrorBlock
          title="Could not load the prepared materials"
          message={pack.error}
          onRetry={pack.retry}
        />
      ) : (
        <div className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4">
          <p className="text-xs wrap-break-word text-muted-foreground">
            Pack v{pack.data.version} · {formatDate(pack.data.createdAt)} · sha{" "}
            {shortSha(pack.data.contentSha256)}
          </p>
          <div className="flex min-w-0 flex-col gap-3 text-sm wrap-break-word">
            <p className="whitespace-pre-wrap">
              {pack.data.manifest.draft.focus.text}
            </p>
            {pack.data.manifest.draft.cover.map((line, index) => (
              <p key={index} className="whitespace-pre-wrap">
                {line.text}
              </p>
            ))}
            {pack.data.manifest.draft.answers.map((answer, index) => (
              <div key={index} className="flex min-w-0 flex-col gap-1">
                <p className="font-medium wrap-break-word">{answer.question}</p>
                {answer.lines.map((line, lineIndex) => (
                  <p key={lineIndex} className="whitespace-pre-wrap">
                    {line.text}
                  </p>
                ))}
              </div>
            ))}
          </div>
          {pack.data.manifest.draft.materialUnknowns.length > 0 ? (
            <p className="text-sm wrap-break-word text-muted-foreground">
              Unknown:{" "}
              {pack.data.manifest.draft.materialUnknowns.join(", ")}
            </p>
          ) : null}
          <p className="flex gap-3 text-xs">
            <a
              className="underline outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              href={`/api/v1/application-packs/${encodeURIComponent(pack.data.id)}/pdf`}
            >
              Pack PDF
            </a>
            <a
              className="underline outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              href={`/api/v1/application-packs/${encodeURIComponent(pack.data.id)}/source.zip`}
            >
              Sources (.zip)
            </a>
          </p>
        </div>
      )}
    </section>
  )
}

function DirectEditSection({
  jobId,
  current,
  onSaved,
}: {
  jobId: string
  current: MaterialVersion
  onSaved: (version: MaterialVersion) => void
}) {
  const pack = useRead(
    `prepare:${jobId}:edit-pack:${current.packId}:v${current.version}`,
    (signal) => getApplicationPack(current.packId, signal)
  )
  return (
    <section
      aria-label="Direct edit"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <h2 className="text-base font-semibold wrap-break-word">Direct edit</h2>
      <p className="text-sm wrap-break-word text-muted-foreground">
        Replace the complete material text exactly as typed. Saving uses no
        model, creates v{current.version + 1}, and invalidates any earlier
        review; the new version needs a fresh review before sending.
      </p>
      {pack.status === "loading" ? (
        <LoadingBlock label="Loading current rendering…" />
      ) : pack.status === "error" ? (
        <ErrorBlock
          title="Could not load the current rendering"
          message={pack.error}
          onRetry={pack.retry}
        />
      ) : (
        <DirectEditBox
          jobId={jobId}
          current={current}
          seedText={renderPackText(pack.data)}
          onSaved={onSaved}
        />
      )}
    </section>
  )
}

function DirectEditBox({
  jobId,
  current,
  seedText,
  onSaved,
}: {
  jobId: string
  current: MaterialVersion
  seedText: string
  onSaved: (version: MaterialVersion) => void
}) {
  const edit = useMaterialEdit({
    jobId,
    expectedVersion: current.version,
    initialText: "",
    onSaved,
  })
  const boxId = `prepare-edit-${current.version}`
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex min-w-0 flex-col gap-2">
        <label htmlFor={boxId} className="text-sm font-medium wrap-break-word">
          Material text (replaces v{current.version} byte-exact)
        </label>
        <Textarea
          id={boxId}
          value={edit.text}
          onChange={(event) => edit.setText(event.target.value)}
          rows={8}
          placeholder="The box starts empty; seed it from the current rendering or type the complete replacement."
          aria-describedby={`${boxId}-status`}
        />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          variant="secondary"
          disabled={edit.saving || edit.text === seedText}
          onClick={() => edit.setText(seedText)}
        >
          Start from current rendering
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={!edit.canSave}
          onClick={edit.save}
        >
          {edit.saving ? "Saving…" : "Save exact edit"}
        </Button>
        <p
          id={`${boxId}-status`}
          className="text-sm wrap-break-word text-muted-foreground"
        >
          {edit.saving
            ? "Saving…"
            : edit.savedVersion !== null && !edit.dirty
              ? `Saved as v${edit.savedVersion}.`
              : edit.unavailableReason ?? "Unsaved changes."}{" "}
          {countBytes(edit.text).toLocaleString("en-US")} bytes.
        </p>
      </div>
      {edit.error !== null ? (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {edit.error}
        </p>
      ) : null}
    </div>
  )
}

function RewriteSection({
  jobId,
  current,
  onRewritten,
}: {
  jobId: string
  current: MaterialVersion
  onRewritten: (version: MaterialVersion) => void
}) {
  const rewrite = useMaterialRewrite({
    jobId,
    expectedVersion: current.version,
    onRewritten,
  })
  const boxId = `prepare-rewrite-${current.version}`
  const runeCount = countRunes(rewrite.instruction)
  return (
    <section
      aria-label="Request rewrite"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Request rewrite
      </h2>
      <p className="text-sm wrap-break-word text-muted-foreground">
        Run one explicit rewrite turn over v{current.version}. The result is a
        new reviewable version; this page never rewrites on its own, and the
        instruction below is the only guidance the turn receives beyond the
        saved facts.
      </p>
      <div className="flex min-w-0 flex-col gap-2">
        <label htmlFor={boxId} className="text-sm font-medium wrap-break-word">
          Instruction (optional, at most{" "}
          {REWRITE_INSTRUCTION_RUNE_LIMIT.toLocaleString("en-US")} characters)
        </label>
        <Textarea
          id={boxId}
          value={rewrite.instruction}
          onChange={(event) => rewrite.setInstruction(event.target.value)}
          rows={3}
          placeholder="Leave blank for a straight rewrite."
          aria-describedby={`${boxId}-status`}
        />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={!rewrite.canRewrite}
          onClick={rewrite.rewrite}
        >
          {rewrite.running ? "Rewriting…" : "Request rewrite"}
        </Button>
        <p
          id={`${boxId}-status`}
          className="text-sm wrap-break-word text-muted-foreground"
        >
          {rewrite.running
            ? "Rewriting…"
            : rewrite.rewrittenVersion !== null
              ? `Rewrite created v${rewrite.rewrittenVersion}.`
              : rewrite.unavailableReason ?? "Ready when you are."}{" "}
          {runeCount.toLocaleString("en-US")} /{" "}
          {REWRITE_INSTRUCTION_RUNE_LIMIT.toLocaleString("en-US")} characters.
        </p>
      </div>
      {rewrite.error !== null ? (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {rewrite.error}
        </p>
      ) : null}
    </section>
  )
}

function AdvanceSection({
  jobId,
  current,
  eligible,
}: {
  jobId: string
  current: MaterialVersion
  eligible: boolean
}) {
  if (!eligible) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        {current.readiness.ready
          ? "This version is outdated, so review waits for a fresh prepare."
          : "This version is held: every required item needs an answer before review."}
      </p>
    )
  }
  return (
    <p>
      <a
        href={`#/applications/${encodeURIComponent(jobId)}/review`}
        className="inline-flex min-h-9 items-center justify-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium whitespace-nowrap text-primary-foreground underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >
        Continue to review v{current.version}
      </a>
    </p>
  )
}
