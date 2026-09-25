import { useEffect, useRef, useState } from "react"
import { useSession } from "@/api/session"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { DiscoverySection } from "@/features/discovery/discovery-section"
import {
  useOwnerContext,
  type EffectiveSavedContext,
} from "@/features/owner-context/useOwnerContext"
import { formatCents } from "@/pages/format"

function FieldCorrectButton({
  label,
  onCorrect,
  disabled,
}: {
  label: string
  onCorrect: () => void
  disabled: boolean
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      aria-label={label}
      onClick={onCorrect}
      disabled={disabled}
    >
      Correct
    </Button>
  )
}

function DesiredWorkPanel({
  context,
  correctionBusy,
  onCorrect,
}: {
  context: EffectiveSavedContext
  correctionBusy: boolean
  onCorrect: (anchor: string) => void
}) {
  const preferences = context.preferences
  const location =
    preferences.preferredLocation === ""
      ? "Not recorded"
      : preferences.preferredLocation
  const timezone =
    preferences.timezone === "" ? "Not recorded" : preferences.timezone
  return (
    <section
      aria-labelledby="your-goals-heading"
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <h2 id="your-goals-heading" className="font-heading text-lg font-medium">
        Your goals
      </h2>
      <p className="mt-1 text-sm text-muted-foreground">
        The saved search profile, exactly as the server returns it. Reading
        this page changes nothing; corrections save through Change my search
        below.
      </p>
      <h3 className="mt-4 font-heading text-base font-medium">
        Profile version {preferences.version}
      </h3>
      <dl className="mt-3 grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <div className="min-w-0">
          <dt className="text-muted-foreground">Preferred location</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span className="wrap-break-word">{location}</span>
            <FieldCorrectButton
              label="Correct preferred location"
              disabled={correctionBusy}
              onCorrect={() =>
                onCorrect(
                  `About preferred location (currently "${location}"): `
                )
              }
            />
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-muted-foreground">Timezone</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span className="wrap-break-word">{timezone}</span>
            <FieldCorrectButton
              label="Correct timezone"
              disabled={correctionBusy}
              onCorrect={() =>
                onCorrect(`About timezone (currently "${timezone}"): `)
              }
            />
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-muted-foreground">Remote</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span>{preferences.allowRemote ? "Allowed" : "Not allowed"}</span>
            <FieldCorrectButton
              label="Correct remote work"
              disabled={correctionBusy}
              onCorrect={() =>
                onCorrect(
                  `About remote work (currently ${preferences.allowRemote ? "allowed" : "not allowed"}): `
                )
              }
            />
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-muted-foreground">Hybrid</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span>{preferences.allowHybrid ? "Allowed" : "Not allowed"}</span>
            <FieldCorrectButton
              label="Correct hybrid work"
              disabled={correctionBusy}
              onCorrect={() =>
                onCorrect(
                  `About hybrid work (currently ${preferences.allowHybrid ? "allowed" : "not allowed"}): `
                )
              }
            />
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-muted-foreground">Target hours per week</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span>{preferences.targetHours}</span>
            <FieldCorrectButton
              label="Correct target hours"
              disabled={correctionBusy}
              onCorrect={() =>
                onCorrect(
                  `About target hours per week (currently ${preferences.targetHours}): `
                )
              }
            />
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-muted-foreground">Minimum monthly base</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-x-2">
            <span>
              {formatCents(
                preferences.minMonthlyBaseCents,
                preferences.salaryCurrency
              )}
            </span>
            <FieldCorrectButton
              label="Correct minimum monthly base"
              disabled={correctionBusy}
              onCorrect={() =>
                onCorrect(
                  `About minimum monthly base (currently ${formatCents(preferences.minMonthlyBaseCents, preferences.salaryCurrency)}): `
                )
              }
            />
          </dd>
        </div>
      </dl>
    </section>
  )
}

function CriteriaPanel({
  context,
  correctionBusy,
  onCorrect,
}: {
  context: EffectiveSavedContext
  correctionBusy: boolean
  onCorrect: (anchor: string) => void
}) {
  return (
    <section aria-labelledby="search-criteria-heading">
      <h2
        id="search-criteria-heading"
        className="font-heading text-lg font-medium"
      >
        Role criteria
      </h2>
      <div className="mt-3">
        {context.requirements.length === 0 ? (
          <EmptyBlock
            title="No role criteria saved"
            description="The server returned an empty role-criteria list for this profile version."
          />
        ) : (
          <ol className="flex min-w-0 flex-col gap-2">
            {context.requirements.map((criterion) => (
              <li
                key={criterion.id}
                className="rounded-md border bg-card px-3 py-2"
              >
                <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-2">
                  <p className="text-sm font-medium wrap-break-word">
                    {criterion.label}
                  </p>
                  <FieldCorrectButton
                    label={`Correct criterion "${criterion.label}"`}
                    disabled={correctionBusy}
                    onCorrect={() =>
                      onCorrect(
                        `About role criterion "${criterion.label}" (${criterion.kind} · ${criterion.mode}): `
                      )
                    }
                  />
                </div>
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
  )
}

function ProfileFactsPanel({ context }: { context: EffectiveSavedContext }) {
  return (
    <section aria-labelledby="profile-facts-heading">
      <h2
        id="profile-facts-heading"
        className="font-heading text-lg font-medium"
      >
        Profile facts
      </h2>
      <p className="mt-1 text-sm text-muted-foreground">
        Facts the search brief carries for this profile. They are derived from
        the saved goals above, not verified CV facts.
      </p>
      <div className="mt-3">
        {context.briefStale ? (
          <Alert className="mb-3">
            <AlertTitle className="wrap-break-word">
              The brief is behind the saved profile
            </AlertTitle>
            <AlertDescription className="wrap-break-word">
              These facts belong to an older profile version. The next Find
              jobs run uses profile version {context.profileVersion}.
            </AlertDescription>
          </Alert>
        ) : null}
        {context.rubricVersion === null ? (
          <EmptyBlock
            title="No search brief yet"
            description="The first Find jobs run authors the brief from this profile."
          />
        ) : context.briefFacts.length === 0 ? (
          <EmptyBlock
            title="No brief facts recorded"
            description={`Brief ${context.rubricVersion} carries no facts for this profile.`}
          />
        ) : (
          <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 rounded-2xl border bg-card px-4 py-4 text-sm sm:grid-cols-2">
            {context.briefFacts.map((fact) => (
              <div key={fact.key} className="min-w-0">
                <dt className="text-muted-foreground wrap-break-word">
                  {fact.key}
                </dt>
                <dd className="wrap-break-word">{fact.value}</dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </section>
  )
}

export function SearchPage() {
  const { session } = useSession()
  const owner = useOwnerContext()
  const [draft, setDraft] = useState("")
  const draftRef = useRef<HTMLTextAreaElement>(null)
  const savedSeenRef = useRef<string | null>(null)

  const correction = owner.correction
  const correctionBusy =
    correction.kind === "sending" || correction.kind === "pending"

  const prefillCorrection = (anchor: string) => {
    setDraft((current) =>
      current === "" ? anchor : `${current.replace(/\s+$/, "")}\n${anchor}`
    )
    draftRef.current?.focus()
  }

  // A confirmed save consumes the draft; the saved status keeps the exact
  // wording that was stored.
  useEffect(() => {
    if (correction.kind !== "saved") return
    if (savedSeenRef.current === correction.requestKey) return
    savedSeenRef.current = correction.requestKey
    setDraft("")
  }, [correction])

  const sendDisabled =
    correctionBusy ||
    owner.context === null ||
    draft.trim() === "" ||
    session === undefined ||
    session === null

  const sendUnavailableReason =
    owner.context === null
      ? "The saved profile is not loaded yet."
      : session === undefined
        ? "Checking dashboard session…"
        : session === null
          ? "Sign in to send a correction."
          : correctionBusy
            ? "A correction is already being saved."
            : draft.trim() === ""
              ? "Describe the change first."
              : null

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">My search</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The saved search profile, exactly as the server returns it. Reading
          this page changes nothing.
        </p>
      </div>

      {owner.contextState === "loading" ? (
        <LoadingBlock label="Loading search preferences…" />
      ) : owner.contextState === "error" || owner.context === null ? (
        <ErrorBlock
          title="Could not load search preferences"
          message={owner.contextError ?? "The request could not be completed."}
          onRetry={owner.retryContext}
        />
      ) : (
        <>
          <DesiredWorkPanel
            context={owner.context}
            correctionBusy={correctionBusy}
            onCorrect={prefillCorrection}
          />
          <CriteriaPanel
            context={owner.context}
            correctionBusy={correctionBusy}
            onCorrect={prefillCorrection}
          />
          <ProfileFactsPanel context={owner.context} />

          <section
            aria-labelledby="change-search-heading"
            className="rounded-2xl border bg-card px-4 py-4"
          >
            <h2
              id="change-search-heading"
              className="font-heading text-lg font-medium"
            >
              Change my search
            </h2>
            <p className="mt-1 text-sm text-muted-foreground">
              State the correction in your own words. It saves against profile
              version {owner.context.profileVersion}; nothing is sent to an
              employer.
            </p>
            <div className="mt-3 flex min-w-0 flex-col gap-2">
              <Label htmlFor="change-search-text">
                Describe the correction in your own words
              </Label>
              <Textarea
                id="change-search-text"
                ref={draftRef}
                rows={4}
                maxLength={20000}
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                placeholder='e.g. "I want remote-only roles in Berlin, at least 32 hours a week"'
                disabled={correctionBusy}
              />
            </div>
            <div className="mt-3 flex min-w-0 flex-wrap items-center gap-2">
              <Button
                type="button"
                disabled={sendDisabled}
                onClick={() => void owner.submitCorrection(draft)}
              >
                {correction.kind === "sending"
                  ? "Sending…"
                  : "Send correction"}
              </Button>
              {sendDisabled && sendUnavailableReason !== null ? (
                <p className="text-xs text-muted-foreground">
                  {sendUnavailableReason}
                </p>
              ) : null}
            </div>

            <div aria-live="polite" className="mt-3">
              {correction.kind === "sending" ? (
                <Alert role="status">
                  <AlertTitle className="wrap-break-word">
                    Sending your correction…
                  </AlertTitle>
                  <AlertDescription className="wrap-break-word">
                    Not accepted yet. Based on profile version{" "}
                    {correction.baseVersion}.
                  </AlertDescription>
                </Alert>
              ) : correction.kind === "pending" ? (
                <Alert role="status">
                  <AlertTitle className="wrap-break-word">
                    {correction.roundState === "paused"
                      ? "Paused — your accepted change is waiting"
                      : "Accepted — saving your change…"}
                  </AlertTitle>
                  <AlertDescription className="wrap-break-word">
                    Accepted, not yet saved. Round {correction.roundId} is{" "}
                    {correction.roundState}; based on profile version{" "}
                    {correction.baseVersion}. The new version appears here only
                    after the saved profile is read back.
                  </AlertDescription>
                  <AlertDescription className="wrap-break-word">
                    Your wording: “{correction.targetText}”
                  </AlertDescription>
                  <div className="col-start-2 mt-2">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => void owner.refreshCorrection()}
                    >
                      Check again
                    </Button>
                  </div>
                </Alert>
              ) : correction.kind === "saved" ? (
                <Alert role="status">
                  <AlertTitle className="wrap-break-word">
                    Saved as profile version {correction.savedVersion}.
                  </AlertTitle>
                  <AlertDescription className="wrap-break-word">
                    The saved profile was read back at version{" "}
                    {correction.savedVersion}; this is now the effective version
                    for the next Find jobs run. Your wording: “
                    {correction.targetText}”
                  </AlertDescription>
                  <div className="col-start-2 mt-2">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={owner.dismissCorrection}
                    >
                      Dismiss
                    </Button>
                  </div>
                </Alert>
              ) : correction.kind === "conflict" ? (
                <ErrorBlock
                  title="The profile changed before your correction was saved"
                  message={`${correction.message} Your change was based on version ${correction.baseVersion}; the saved profile is now version ${correction.currentVersion}.`}
                  onRetry={owner.dismissCorrection}
                  retryLabel="Dismiss"
                />
              ) : correction.kind === "error" ? (
                <ErrorBlock
                  title="Correction failed"
                  message={correction.message}
                  onRetry={
                    correction.retryable
                      ? () => void owner.submitCorrection(draft)
                      : owner.dismissCorrection
                  }
                  retryLabel={correction.retryable ? "Try again" : "Dismiss"}
                />
              ) : null}
            </div>
          </section>
        </>
      )}

      <DiscoverySection museScenario="live" />
    </div>
  )
}
