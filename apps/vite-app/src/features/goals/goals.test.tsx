// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import App from "@/App"
import type { Preferences, SavedAnswerList } from "@/api/client"
import { preferencesFixture, stubFetch } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  window.location.hash = ""
  vi.unstubAllGlobals()
})

const answersFixture: SavedAnswerList = {
  items: [
    {
      id: "ans-1",
      currentVersion: 2,
      scopeTags: ["motivation", "backend"],
      contextNote: "Harbour roles",
      versions: [
        {
          version: 2,
          text: "Six years of Go platform work.",
          textSha256: "x".repeat(64),
          approvedAt: "2026-09-20T10:00:00Z",
          approvedBy: { actorKind: "administrator", actorId: "owner" },
          approvalRequestKey: "req-1",
        },
      ],
    },
  ],
}

function emptyCriteriaPreferences(): Preferences {
  return { ...preferencesFixture, roleCriteria: [] }
}

describe("goals form and experience (B2)", () => {
  it("reviews wants beside sourced experience", async () => {
    stubFetch({ savedAnswers: answersFixture })
    window.location.hash = "#/search"
    render(<App />)

    expect(await screen.findByText("What you want next")).toBeDefined()
    expect(await screen.findByText("Your experience")).toBeDefined()
    expect(await screen.findByText("Backend engineer")).toBeDefined()
    expect(await screen.findByText("On-call rotation")).toBeDefined()
    expect(
      await screen.findByText("Six years of Go platform work.")
    ).toBeDefined()
    expect(
      await screen.findByText(/Scope: motivation, backend/)
    ).toBeDefined()
  })

  it("saves edited choices with the loaded version", async () => {
    const { calls } = stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Edit goals" })
    )
    const label = (await within(wants).findByDisplayValue(
      "Backend engineer"
    )) as HTMLInputElement
    fireEvent.change(label, { target: { value: "Backend engineer (Go)" } })
    fireEvent.click(within(wants).getByRole("button", { name: "Save goals" }))

    const put = calls.find(
      (call) => call.url.endsWith("/api/v1/preferences") && call.method === "PUT"
    )
    expect(put).toBeDefined()
    expect(
      await screen.findByText(/Saved profile version 4/)
    ).toBeDefined()
    expect(await screen.findByText("Backend engineer (Go)")).toBeDefined()
  })

  it("reports a concurrent change instead of overwriting", async () => {
    stubFetch({ preferencesConflict: true })
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Edit goals" })
    )
    fireEvent.click(within(wants).getByRole("button", { name: "Save goals" }))

    expect(
      await within(wants).findByText("The goals changed before your save")
    ).toBeDefined()
  })

  it("opens the form and blocks commissioning with no explicit choices", async () => {
    stubFetch({ preferences: emptyCriteriaPreferences() })
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    expect(
      await within(wants).findByText(/No explicit wants yet/)
    ).toBeDefined()
    expect(
      await within(wants).findByRole("button", { name: "Add a want" })
    ).toBeDefined()
    expect(
      await screen.findByText(/No wants or don't-wants are saved yet/)
    ).toBeDefined()
  })

  it("adds a don't-want without retyping experience", async () => {
    stubFetch({
      preferences: emptyCriteriaPreferences(),
      savedAnswers: answersFixture,
    })
    window.location.hash = "#/search"
    render(<App />)

    expect(
      await screen.findByText("Six years of Go platform work.")
    ).toBeDefined()
    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Add a want" })
    )
    const label = within(wants).getByPlaceholderText("e.g. Backend engineer")
    fireEvent.change(label, { target: { value: "Night shifts" } })
    const selects = within(wants).getAllByLabelText("Want level")
    fireEvent.change(selects[selects.length - 1], {
      target: { value: "avoid" },
    })
    fireEvent.click(within(wants).getByRole("button", { name: "Save goals" }))

    expect(
      await screen.findByText(/Saved profile version 4/)
    ).toBeDefined()
    expect(await screen.findByText("Night shifts")).toBeDefined()
  })
})
