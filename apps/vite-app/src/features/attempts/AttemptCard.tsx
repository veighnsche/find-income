// One exact attempted snapshot: the frozen pack bytes reference, the
// destination, and the actual recorded service result. Renders only the
// immutable delivery item (plus its immutable pack record for the version
// number); never current materials.

import {
  applicationPackPdfUrl,
  getApplicationPack,
  type DeliveryItem,
} from "@/api/client"
import { formatDate } from "@/pages/format"
import { useRead } from "@/pages/useRead"

export function attemptOutcomeText(state: DeliveryItem["state"]): string {
  switch (state) {
    case "accepted_by_smtp":
      return "Accepted by the outgoing SMTP server. Employer receipt is unverified."
    case "uncertain":
      return "Submission outcome unknown. It may have been accepted; do not resend."
    case "sending":
      return "Submission in progress. Its outcome is not yet known."
    case "failed":
      return "Submission failed. This review will not be sent again."
    case "prepared":
      return "Prepared for owner review; no submission attempted."
  }
}

export function AttemptCard({ item }: { item: DeliveryItem }) {
  const pack = useRead(`attempt:${item.packId}:pack`, (signal) =>
    getApplicationPack(item.packId, signal)
  )

  const packLabel =
    pack.status === "ready" ? `Pack v${pack.data.version}` : "Attempted pack"

  return (
    <article
      aria-label={`${packLabel} attempt`}
      className="flex min-w-0 flex-col gap-2 rounded-xl border bg-card px-3 py-2.5"
    >
      <p role="status" className="text-sm wrap-break-word">
        <strong>{attemptOutcomeText(item.state)}</strong>
      </p>
      {!item.current ? (
        <p className="text-xs text-muted-foreground wrap-break-word">
          Newer materials exist since this attempt. The snapshot below is the
          exact attempted version and is unchanged.
        </p>
      ) : null}

      <dl className="flex min-w-0 flex-col gap-1 text-sm">
        <div className="flex min-w-0 flex-col gap-0.5">
          <dt className="text-xs font-medium text-muted-foreground">
            Attempted pack
          </dt>
          <dd className="wrap-break-word">
            {packLabel}
            {pack.status === "ready" ? (
              <span
                className="text-xs text-muted-foreground"
                title={pack.data.createdAt}
              >
                {" "}
                · saved {formatDate(pack.data.createdAt)}
              </span>
            ) : null}{" "}
            <a
              href={applicationPackPdfUrl(item.packId)}
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              Open attempted pack PDF
            </a>
          </dd>
          <dd
            className="truncate font-mono text-xs text-muted-foreground"
            title={`Attempted pack content SHA-256: ${item.packContentSha256}`}
          >
            {item.packContentSha256}
          </dd>
        </div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <dt className="text-xs font-medium text-muted-foreground">To</dt>
          <dd className="wrap-break-word">{item.recipient}</dd>
        </div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <dt className="text-xs font-medium text-muted-foreground">From</dt>
          <dd className="wrap-break-word">{item.sender}</dd>
        </div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <dt className="text-xs font-medium text-muted-foreground">Subject</dt>
          <dd className="wrap-break-word">{item.subject}</dd>
        </div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <dt className="text-xs font-medium text-muted-foreground">
            Route evidence
          </dt>
          <dd className="wrap-break-word">
            {item.routeExcerpt === ""
              ? "No excerpt recorded."
              : item.routeExcerpt}
          </dd>
        </div>
      </dl>

      {(item.attemptId !== undefined && item.attemptId !== "") ||
      (item.outcomeDetail !== undefined && item.outcomeDetail !== "") ||
      (item.smtpStage !== undefined && item.smtpStage !== "") ||
      item.smtpCode !== undefined ? (
        <p className="text-xs text-muted-foreground wrap-break-word">
          {item.smtpStage !== undefined && item.smtpStage !== ""
            ? `SMTP ${item.smtpStage}`
            : "SMTP stage unknown"}
          {item.smtpCode !== undefined ? ` · code ${item.smtpCode}` : ""}
          {item.outcomeDetail !== undefined && item.outcomeDetail !== ""
            ? ` · ${item.outcomeDetail}`
            : ""}
          {item.attemptId !== undefined && item.attemptId !== ""
            ? ` · attempt ${item.attemptId}`
            : ""}
        </p>
      ) : null}

      <details>
        <summary className="cursor-pointer text-sm font-medium underline-offset-4 hover:underline">
          Exact attempted message body
        </summary>
        <pre className="mt-1 max-h-96 overflow-auto rounded-md border bg-muted/40 p-2 text-xs whitespace-pre-wrap wrap-break-word">
          {item.body}
        </pre>
      </details>

      <details>
        <summary className="cursor-pointer text-sm font-medium underline-offset-4 hover:underline">
          Attempt audit details
        </summary>
        <div className="mt-1 flex min-w-0 flex-col gap-1 text-xs text-muted-foreground wrap-break-word">
          <p>
            Pack <code className="font-mono">{item.packId}</code> · route{" "}
            <code className="font-mono">{item.routeId}</code> revision{" "}
            {item.routeRevision} · role revision {item.opportunityRevision} ·
            profile revision {item.profileRevision}
          </p>
          <p className="wrap-break-word">
            Attached PDF SHA-256{" "}
            <code className="font-mono wrap-break-word">
              {item.attachmentSha256}
            </code>
          </p>
          <p className="wrap-break-word">
            Canonical MIME SHA-256{" "}
            <code className="font-mono wrap-break-word">{item.mimeSha256}</code>{" "}
            · message ID{" "}
            <code className="font-mono wrap-break-word">{item.messageId}</code>
          </p>
          {item.roundId !== undefined && item.roundId !== "" ? (
            <p>
              Delivery round{" "}
              <code className="font-mono">{item.roundId}</code>
            </p>
          ) : null}
        </div>
      </details>
    </article>
  )
}
