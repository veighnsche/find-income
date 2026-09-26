// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"
import { updatePreferences } from "@/api/client"
import { SessionProvider } from "@/api/session"
import {
  notifyGoalsAccepted,
  SavedGoalsProvider,
  useSavedGoals,
} from "@/components/shared/saved-goals"
import {
  preferencesFixture,
  sessionFixture,
  stubFetch,
} from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function VersionProbe({ label }: { label: string }) {
  const { state, version } = useSavedGoals()
  return (
    <p>
      {label}: {state} v{version === null ? "?" : version}
    </p>
  )
}

describe("shared saved-goal context", () => {
  it("shares one accepted read between Search and discovery consumers", async () => {
    const { calls } = stubFetch()
    render(
      <SessionProvider>
        <SavedGoalsProvider>
          <VersionProbe label="search" />
          <VersionProbe label="discovery" />
        </SavedGoalsProvider>
      </SessionProvider>
    )
    await screen.findByText("search: ready v3")
    await screen.findByText("discovery: ready v3")
    expect(
      calls.filter(
        (call) => call.url === "/api/v1/preferences" && call.method === "GET"
      ).length
    ).toBe(1)
  })

  it("updates every consumer immediately after an accepted goal write", async () => {
    stubFetch()
    render(
      <SessionProvider>
        <SavedGoalsProvider>
          <VersionProbe label="search" />
          <VersionProbe label="discovery" />
        </SavedGoalsProvider>
      </SessionProvider>
    )
    await screen.findByText("search: ready v3")

    const { version, ...input } = preferencesFixture
    await act(async () => {
      await updatePreferences(
        { ...input, expectedVersion: version },
        sessionFixture.csrfToken
      )
      notifyGoalsAccepted()
    })

    await screen.findByText("search: ready v4")
    await screen.findByText("discovery: ready v4")
  })

  it("requires the provider so no consumer reads a split-brain copy", () => {
    stubFetch()
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {})
    try {
      expect(() =>
        render(
          <SessionProvider>
            <VersionProbe label="orphan" />
          </SessionProvider>
        )
      ).toThrow("useSavedGoals must be used inside SavedGoalsProvider")
    } finally {
      consoleError.mockRestore()
    }
  })
})
