// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { Preferences } from "@/api/client"
import {
  changeSearchCorrectionBoxId,
  SavedBriefPanel,
  summarizeSavedBriefTerms,
  type SavedBriefPanelProps,
} from "@/features/discovery/saved-brief"
import type { EffectiveSavedContext } from "@/features/owner-context/useOwnerContext"

const preferencesFixture: Preferences = {
  version: 3,
  preferredLocation: "Berlin",
  allowRemote: true,
  allowHybrid: false,
  targetHours: "32",
  minMonthlyBaseCents: 450000,
  salaryCurrency: "EUR",
  timezone: "Europe/Berlin",
  roleCriteria: [
    {
      id: "criterion-1",
      label: "Backend engineer",
      description: "Server-side product work.",
      kind: "role",
      mode: "require",
      definitionHash: "hash-1",
    },
  ],
}

function contextFixture(
  overrides: Partial<EffectiveSavedContext> = {}
): EffectiveSavedContext {
  return {
    profileVersion: 3,
    rubricVersion: "criteria-v3-abc123def456",
    rubricSource: "preferences_versions:current:v3",
    summary: "Profile v3 · Berlin · remote ok · 1 criterion",
    preferences: preferencesFixture,
    briefFacts: [{ key: "preferredLocation", value: "Berlin" }],
    requirements: preferencesFixture.roleCriteria,
    catalogVersion: null,
    briefStale: false,
    ...overrides,
  }
}

function renderPanel(props: Partial<SavedBriefPanelProps> = {}) {
  const retryContext = vi.fn()
  render(
    <SavedBriefPanel
      context={contextFixture()}
      contextState="ready"
      contextError={null}
      retryContext={retryContext}
      {...props}
    />
  )
  return { retryContext }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("saved brief", () => {
  it("summarizes concise saved terms without inference", () => {
    expect(summarizeSavedBriefTerms(preferencesFixture)).toBe(
      "Berlin · remote ok · 32 h/week · at least EUR 4500.00/mo · 1 role criterion"
    )
    expect(
      summarizeSavedBriefTerms({
        ...preferencesFixture,
        preferredLocation: "",
        allowRemote: false,
        allowHybrid: false,
        minMonthlyBaseCents: 0,
        roleCriteria: [],
      })
    ).toBe(
      "location not recorded · onsite only · 32 h/week · no pay floor · 0 role criteria"
    )
  })

  it("shows version identity and concise terms, reading nothing itself", () => {
    renderPanel()

    screen.getByText("Saved search brief")
    screen.getByText("Profile v3 · criteria-v3-abc123def456")
    screen.getByText("preferences_versions:current:v3")
    screen.getByText(
      "Berlin · remote ok · 32 h/week · at least EUR 4500.00/mo · 1 role criterion"
    )
    screen.getByText("Backend engineer — role · require")
    const changeLink = screen.getByRole("link", {
      name: "Change my search",
    }) as HTMLAnchorElement
    expect(changeLink.getAttribute("href")).toBe("#/search")
  })

  it("treats a missing catalog as normal pre-first-run state", () => {
    renderPanel()
    screen.getByText(/No reason catalog yet/)
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("shows the catalog version when one is authored", () => {
    renderPanel({
      context: contextFixture({ catalogVersion: "catalog-v3-1" }),
    })
    screen.getByText("Reason catalog catalog-v3-1 ready.")
  })

  it("shows an empty state when no brief exists yet", () => {
    renderPanel({
      context: contextFixture({ rubricVersion: null, rubricSource: null }),
    })

    screen.getByText("No saved search brief yet")
    screen.getByText(/Profile v3 is saved/)
    screen.getByText(/The first Find jobs run authors one/)
    screen.getByRole("link", { name: "Change my search" })
  })

  it("shows a stale brief honestly with a refresh action", () => {
    const { retryContext } = renderPanel({
      context: contextFixture({
        profileVersion: 4,
        preferences: { ...preferencesFixture, version: 4 },
        briefStale: true,
      }),
    })

    const alert = screen.getByRole("alert")
    expect(alert.textContent).toContain(
      "The saved brief is behind profile v4."
    )
    fireEvent.click(screen.getByRole("button", { name: "Refresh saved brief" }))
    expect(retryContext).toHaveBeenCalledTimes(1)
  })

  it("reports a run commissioned against an older brief", () => {
    renderPanel({ runBriefProfileVersion: 2 })
    screen.getByText(
      "The run below used brief profile v2; the saved brief is now profile v3."
    )
  })

  it("stays silent about the run basis when versions agree", () => {
    renderPanel({ runBriefProfileVersion: 3 })
    screen.getByText("Saved search brief")
    expect(screen.queryByText(/The run below used brief/)).toBeNull()
  })

  it("surfaces a context read failure with a retry", () => {
    const { retryContext } = renderPanel({
      context: null,
      contextState: "error",
      contextError: "Prefs down.",
    })
    screen.getByText("Could not load the saved search brief")
    screen.getByText("Prefs down.")
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(retryContext).toHaveBeenCalledTimes(1)
  })

  it("shows a loading state while the context reads", () => {
    renderPanel({ context: null, contextState: "loading" })
    screen.getByText("Loading saved search brief…")
  })

  it("routes Change my search into the correction box", async () => {
    render(
      <>
        <SavedBriefPanel
          context={contextFixture()}
          contextState="ready"
          contextError={null}
          retryContext={() => {}}
        />
        <textarea
          id={changeSearchCorrectionBoxId}
          aria-label="correction box"
        />
      </>
    )

    fireEvent.click(screen.getByRole("link", { name: "Change my search" }))
    const box = screen.getByLabelText("correction box")
    await waitFor(() => expect(document.activeElement).toBe(box))
  })

  it("honors a change-search href override", () => {
    renderPanel({ changeSearchHref: "#/search?from=jobs" })
    const changeLink = screen.getByRole("link", {
      name: "Change my search",
    }) as HTMLAnchorElement
    expect(changeLink.getAttribute("href")).toBe("#/search?from=jobs")
  })
})
