import { useEffect, useState } from "react"
import {
  RequestError,
  applicationPackPdfUrl,
  applicationPackSourceUrl,
  getApplicationPack,
  getCurrentOpportunityMaterials,
  getCurrentQuestionAnswers,
  getOpportunity,
  isUnauthenticated,
  listApplicationPacks,
  listOpportunityRoutes,
} from "@/api/client"
import { useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  UnsupportedBlock,
} from "@/components/shared"
import { ReviewAuthorization } from "@/features/review/ReviewAuthorization"
import { SendReview } from "@/features/review/SendReview"
import type { DeliveryReview, MaterialStatusView, MaterialVersion } from "@/api/client"
import { formatDate } from "@/pages/format"

function materialOriginLabel(origin: MaterialVersion["provenance"]["origin"]): string {
  switch (origin) {
    case "prepared":
      return "Prepared from verified facts"
    case "direct_edit":
      return "Direct owner edit (no model)"
    case "rewrite":
      return "Explicit rewrite"
  }
}

type Section<T> =
  | { status: "loading" }
  | { status: "ready"; data: T }
  | { status: "error"; message: string; unavailable: boolean }

function useSection<T>(key: string, load: (signal: AbortSignal) => Promise<T>): Section<T> {
  const { loseSession } = useSession()
  const [lastKey, setLastKey] = useState(key)
  const [section, setSection] = useState<Section<T>>({ status: "loading" })
  if (lastKey !== key) {
    setLastKey(key)
    setSection({ status: "loading" })
  }
  useEffect(() => {
    const controller = new AbortController()
    load(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setSection({ status: "ready", data })
      },
      (cause: unknown) => {
        if (controller.signal.aborted) return
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setSection({
          status: "error",
          message: cause instanceof Error ? cause.message : "The request could not be completed.",
          unavailable:
            cause instanceof RequestError && (cause.status === 503 || cause.status === 404),
        })
      }
    )
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  if (lastKey !== key) return { status: "loading" }
  return section
}

export interface ReviewPageProps {
  jobId: string
}

export function ReviewPage({ jobId }: ReviewPageProps) {
  const opportunity = useSection(`opportunity:${jobId}`, (signal) => getOpportunity(jobId, signal))
  const routes = useSection(`routes:${jobId}`, (signal) => listOpportunityRoutes(jobId, signal))
  const packs = useSection(`packs:${jobId}`, (signal) => listApplicationPacks(jobId, signal))
  const answers = useSection(`answers:${jobId}`, (signal) => getCurrentQuestionAnswers(jobId, signal))
  const materials = useSection(`materials:${jobId}`, (signal) =>
    getCurrentOpportunityMaterials(jobId, signal)
  )
  const [sendContext, setSendContext] = useState<{ jobId: string; review: DeliveryReview | null }>({
    jobId,
    review: null,
  })
  if (sendContext.jobId !== jobId) {
    setSendContext({ jobId, review: null })
  }
  if (opportunity.status === "loading") return <LoadingBlock label="Loading review" />
  if (opportunity.status === "error")
    return <ErrorBlock title="Review unavailable" message={opportunity.message} />
  const role = opportunity.data

  const rolePacks =
    packs.status === "ready"
      ? packs.data
      : []
  const latestPack =
    rolePacks.length > 0
      ? rolePacks.reduce((a, b) => (a.version > b.version ? a : b))
      : null

  return (
    <div className="space-y-6">
      <p className="flex min-w-0 flex-wrap gap-x-4 gap-y-1">
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/prepare`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to prepare
        </a>
        <a
          href={`#/applications/${encodeURIComponent(jobId)}`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Application attempts
        </a>
      </p>
      <div>
        <h1 className="text-2xl font-semibold">Review application</h1>
        <p className="text-muted-foreground mt-1 text-sm">
          {role.opportunity.title || "(untitled role)"} · opportunity revision {role.opportunity.revision}
        </p>
      </div>

      <section aria-label="Destination">
        <h2 className="text-lg font-medium">Destination</h2>
        {routes.status === "loading" && <LoadingBlock label="Loading routes" />}
        {routes.status === "error" &&
          (routes.unavailable ? (
            <UnsupportedBlock title="No saved destination" message="No application route is saved for this role yet." />
          ) : (
            <ErrorBlock title="Routes unavailable" message={routes.message} />
          ))}
        {routes.status === "ready" &&
          (routes.data.length === 0 ? (
            <EmptyBlock title="No destination saved" description="Check job details saves the application route." />
          ) : (
            <ul className="mt-2 space-y-2">
              {routes.data.map((route) => (
                <li key={route.id} className="rounded-md border p-3 text-sm">
                  <span className="font-medium">{route.kind}</span>
                  <span className="text-muted-foreground"> · {route.destinationText}</span>
                  <span className="text-muted-foreground block text-xs">
                    route {route.id} · revision {route.revision}
                  </span>
                </li>
              ))}
            </ul>
          ))}
      </section>

      <section aria-label="Exact answers">
        <h2 className="text-lg font-medium">Exact answers</h2>
        {answers.status === "loading" && <LoadingBlock label="Loading answers" />}
        {answers.status === "error" &&
          (answers.unavailable ? (
            <UnsupportedBlock title="Answers not ready" message="Answer questions before reviewing." />
          ) : (
            <ErrorBlock title="Answers unavailable" message={answers.message} />
          ))}
        {answers.status === "ready" && (
          <ul className="mt-2 space-y-2">
            {answers.data.values.map((value) => (
              <li key={value.questionId} className="rounded-md border p-3 text-sm">
                <span className="text-muted-foreground block text-xs">
                  {value.required} · {value.state} · answer v{value.version}
                </span>
                {value.state === "answered" ? (
                  <span className="whitespace-pre-wrap">{value.text}</span>
                ) : (
                  <span className="text-muted-foreground italic">left blank</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-label="Application pack">
        <h2 className="text-lg font-medium">Application pack</h2>
        {packs.status === "loading" && <LoadingBlock label="Loading packs" />}
        {packs.status === "error" && (
          <ErrorBlock title="Packs unavailable" message={packs.message} />
        )}
        {packs.status === "ready" &&
          (latestPack === null ? (
            <EmptyBlock title="No pack prepared" description="Prepare applications to create the first pack version." />
          ) : (
            <PackDetail packId={latestPack.id} />
          ))}
      </section>

      <section aria-label="Material version">
        <h2 className="text-lg font-medium">Material version</h2>
        {materials.status === "loading" && <LoadingBlock label="Loading materials" />}
        {materials.status === "error" &&
          (materials.unavailable ? (
            <UnsupportedBlock title="Materials not prepared" message="Prepare applications to version the materials." />
          ) : (
            <ErrorBlock title="Materials unavailable" message={materials.message} />
          ))}
        {materials.status === "ready" &&
          (materials.data.current === undefined || materials.data.current === null ? (
            <EmptyBlock
              title="No material version"
              description="The server reports no current version for this status. Prepare applications to create one."
            />
          ) : (
            <MaterialVersionDetail view={materials.data} current={materials.data.current} />
          ))}
      </section>

      <section aria-label="Review state">
        <h2 className="text-lg font-medium">Review state</h2>
        {materials.status === "loading" || packs.status === "loading" ? (
          <LoadingBlock label="Loading authorization state" />
        ) : (
          <ReviewAuthorization
            packId={latestPack?.id ?? null}
            onReviewChange={(next) => setSendContext({ jobId, review: next })}
            materialReady={
              materials.status === "ready" &&
              materials.data.current !== undefined &&
              materials.data.current !== null &&
              materials.data.status === "prepared" &&
              materials.data.current.readiness.ready
            }
            materialVersion={materials.status === "ready" ? (materials.data.current?.version ?? null) : null}
            materialStatus={materials.status === "ready" ? materials.data.status : null}
            prepareHref={`#/jobs/${encodeURIComponent(jobId)}/prepare`}
          />
        )}
      </section>

      <section aria-label="Send">
        <h2 className="text-lg font-medium">Send</h2>
        {sendContext.jobId === jobId &&
        sendContext.review !== null &&
        sendContext.review.approvedAt !== undefined &&
        sendContext.review.approvedAt !== null ? (
          <SendReview
            key={sendContext.review.id}
            jobId={jobId}
            review={sendContext.review}
            onReviewChange={(next) => setSendContext({ jobId, review: next })}
          />
        ) : (
          <p className="text-muted-foreground mt-2 text-sm">
            Approve the review above to enable the explicit send step. Nothing is sent
            automatically.
          </p>
        )}
      </section>
    </div>
  )
}

function MaterialVersionDetail({
  view,
  current,
}: {
  view: MaterialStatusView
  current: MaterialVersion
}) {
  const held = current.readiness.held.filter(
    (id) => !current.readiness.missingRequired.includes(id)
  )
  return (
    <div className="mt-2 space-y-2 rounded-md border p-3 text-sm">
      <p>
        <span className="font-medium">v{current.version}</span>
        <span className="text-muted-foreground">
          {" "}
          · {view.status} · {current.readiness.ready ? "ready" : "not ready"}
        </span>
      </p>
      <p className="text-muted-foreground text-xs">
        {materialOriginLabel(current.provenance.origin)}
        {current.provenance.origin === "rewrite" &&
        current.provenance.rewriteOf !== undefined
          ? ` of v${current.provenance.rewriteOf}`
          : ""}
        {" · "}
        {formatDate(current.createdAt)} by {current.createdBy.actorId} (
        {current.createdBy.actorKind})
      </p>
      <p className="text-muted-foreground text-xs">
        role r{current.opportunityRevision} · profile r{current.profileRevision} ·
        check {current.checkId} · pack {current.packId}
      </p>
      {current.readiness.missingRequired.length > 0 && (
        <p className="text-xs">
          missing: {current.readiness.missingRequired.join(", ")}
        </p>
      )}
      {held.length > 0 && (
        <p className="text-xs">held: {held.join(", ")}</p>
      )}
      <p className="text-muted-foreground text-xs">
        {current.provenance.sourceShas.length === 0
          ? "No source SHAs recorded."
          : `${current.provenance.sourceShas.length} source ${current.provenance.sourceShas.length === 1 ? "SHA" : "SHAs"}: ${current.provenance.sourceShas.join(", ")}`}
      </p>
    </div>
  )
}

function PackDetail({ packId }: { packId: string }) {
  const pack = useSection(`pack:${packId}`, (signal) => getApplicationPack(packId, signal))
  if (pack.status === "loading") return <LoadingBlock label="Loading pack" />
  if (pack.status === "error")
    return <ErrorBlock title="Pack unavailable" message={pack.message} />
  const detail = pack.data
  return (
    <div className="mt-2 space-y-2 rounded-md border p-3 text-sm">
      <p>
        <span className="font-medium">v{detail.version}</span>
        <span className="text-muted-foreground">
          {" "}
          · {formatDate(detail.createdAt)} · sha {detail.contentSha256.slice(0, 12)}…
        </span>
      </p>
      {detail.manifest.draft.materialUnknowns.length > 0 && (
        <p className="text-xs">
          unknown: {detail.manifest.draft.materialUnknowns.join(", ")}
        </p>
      )}
      <p className="flex gap-3 text-xs">
        <a className="underline" href={applicationPackPdfUrl(detail.id)}>
          Pack PDF
        </a>
        <a className="underline" href={applicationPackSourceUrl(detail.id)}>
          Sources (.zip)
        </a>
      </p>
    </div>
  )
}
