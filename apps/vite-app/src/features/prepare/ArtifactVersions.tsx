import { useState } from "react"
import {
  exportOpportunityArtifact,
  isUnauthenticated,
  listArtifactVersions,
  type ArtifactView,
} from "@/api/client"
import { useSession } from "@/api/session"
import { Button } from "@/components/ui/button"
import type { StoredArtifactType } from "@/features/prepare/artifactsApi"
import {
  artifactTypeLabel,
  copyText,
  downloadBlob,
} from "@/features/prepare/ArtifactsSection"
import { mutationMessage } from "@/features/prepare/useDraftArtifacts"
import { formatDate } from "@/pages/format"

// VersionsPanel is the E2/E3 inspectable history for one stored artifact.
// Opening it issues one explicit GET (never a model call or write) and
// lists every stored version oldest first. Selecting a version shows its
// bytes with an honest current/previous label, and copy/download act on
// the SHOWN version: downloads render server-side from stored content with
// identity/version/checksum headers and job/item/version filenames.
export function VersionsPanel({
  jobId,
  artifactType,
  current,
}: {
  jobId: string
  artifactType: StoredArtifactType
  current: ArtifactView
}) {
  const { loseSession } = useSession()
  const [versions, setVersions] = useState<ArtifactView[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<number | null>(null)
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">(
    "idle"
  )
  const [exportState, setExportState] = useState<
    "idle" | "working" | "failed"
  >("idle")

  const shown =
    selected === null
      ? current
      : (versions?.find((version) => version.version === selected) ?? current)
  const isCurrent = shown.version === current.version
  const label = artifactTypeLabel(artifactType)

  async function load() {
    if (loading) return
    setLoading(true)
    setError(null)
    try {
      const page = await listArtifactVersions(jobId, artifactType)
      setLoading(false)
      setVersions(page.items)
      setSelected(null)
    } catch (cause: unknown) {
      setLoading(false)
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(mutationMessage(cause))
    }
  }

  async function copyShown() {
    setExportState("idle")
    setCopyState((await copyText(shown.content)) ? "copied" : "failed")
  }

  async function downloadShown() {
    if (exportState === "working") return
    setCopyState("idle")
    setExportState("working")
    try {
      const exported = await exportOpportunityArtifact(
        jobId,
        artifactType,
        shown.version
      )
      const ok = downloadBlob(
        exported.filename,
        exported.text,
        exported.mediaType === "" ? "text/plain" : exported.mediaType
      )
      setExportState(ok ? "idle" : "failed")
    } catch (cause: unknown) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setExportState("failed")
    }
  }

  if (versions === null) {
    return (
      <div className="flex min-w-0 flex-col gap-2">
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => void load()}
          >
            {loading ? "Loading versions…" : "Show version history"}
          </Button>
        </div>
        {error === null ? null : (
          <p role="alert" className="text-sm wrap-break-word text-destructive">
            {error}
          </p>
        )}
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <p className="text-sm font-medium wrap-break-word">
        {`Version history (${versions.length} stored, oldest first)`}
      </p>
      {versions.length === 0 ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          The server reports no stored versions for this item.
        </p>
      ) : (
        <ul className="flex min-w-0 flex-wrap gap-2">
          {versions.map((version) => {
            const active = version.version === shown.version
            return (
              <li key={version.version}>
                <Button
                  type="button"
                  variant={active ? "default" : "outline"}
                  size="sm"
                  aria-pressed={active}
                  aria-label={`Show ${label} v${version.version}`}
                  onClick={() => {
                    setSelected(version.version)
                    setCopyState("idle")
                    setExportState("idle")
                  }}
                >
                  {`v${version.version} · ${formatDate(version.createdAt)}`}
                </Button>
              </li>
            )
          })}
        </ul>
      )}
      <p className="text-xs wrap-break-word text-muted-foreground">
        {isCurrent
          ? `Showing v${shown.version} (current) by ${shown.createdBy.actorId} (${shown.createdBy.actorKind}) · ${formatDate(shown.createdAt)}.`
          : `Showing v${shown.version} (previous version — read-only) by ${shown.createdBy.actorId} (${shown.createdBy.actorKind}) · ${formatDate(shown.createdAt)}. The exact editor above always edits the current version.`}
      </p>
      <pre className="min-w-0 overflow-x-auto rounded-md border border-border bg-muted/40 p-3 text-sm wrap-break-word whitespace-pre-wrap">
        {shown.content}
      </pre>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={`Copy shown ${label} v${shown.version}`}
          onClick={() => void copyShown()}
        >
          Copy shown
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={`Download shown ${label} v${shown.version}`}
          disabled={exportState === "working"}
          onClick={() => void downloadShown()}
        >
          {exportState === "working" ? "Exporting…" : "Download shown"}
        </Button>
        {copyState === "copied" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Copied.
          </p>
        ) : copyState === "failed" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Copy failed — select the text manually.
          </p>
        ) : null}
        {exportState === "failed" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Export failed — try again or copy the text instead.
          </p>
        ) : null}
      </div>
    </div>
  )
}
