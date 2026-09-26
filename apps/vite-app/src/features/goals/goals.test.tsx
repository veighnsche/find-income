// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
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

  it("edits salary in ordinary units and saves exact cents", async () => {
    stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Edit goals" })
    )
    // The saved 450000 cents show as ordinary units, never raw cents.
    const base = (await within(wants).findByLabelText(
      "Minimum monthly base (EUR)"
    )) as HTMLInputElement
    expect(base.value).toBe("4500.00")
    fireEvent.change(base, { target: { value: "4500.50" } })
    fireEvent.click(within(wants).getByRole("button", { name: "Save goals" }))

    // The save readback remounts the panel (as before); re-query the region.
    expect(
      await screen.findByText(/Saved profile version 4/)
    ).toBeDefined()
    const review = await screen.findByRole("region", {
      name: "What you want next",
    })
    // The stub merges the PUT body, so the review proves the exact cents.
    expect(await within(review).findByText(/4,500\.50 EUR\/mo/)).toBeDefined()
  })

  it("rejects an over-precise salary without saving", async () => {
    const { calls } = stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Edit goals" })
    )
    const base = await within(wants).findByLabelText(
      "Minimum monthly base (EUR)"
    )
    fireEvent.change(base, { target: { value: "45.505" } })
    fireEvent.click(within(wants).getByRole("button", { name: "Save goals" }))

    expect(
      await within(wants).findByText(/must be an amount like 4500/)
    ).toBeDefined()
    expect(
      calls.some(
        (call) =>
          call.url.endsWith("/api/v1/preferences") && call.method === "PUT"
      )
    ).toBe(false)
  })

  it("refreshes Find jobs readiness immediately after the first save", async () => {
    stubFetch({ preferences: emptyCriteriaPreferences() })
    window.location.hash = "#/search"
    render(<App />)

    // Blocked before the first explicit want exists.
    expect(
      await screen.findByText(/No wants or don't-wants are saved yet/)
    ).toBeDefined()

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Add a want" })
    )
    const label = within(wants).getByPlaceholderText("e.g. Backend engineer")
    fireEvent.change(label, { target: { value: "Backend engineer" } })
    fireEvent.click(within(wants).getByRole("button", { name: "Save goals" }))

    // The shared saved context updates discovery readiness without a reload.
    expect(
      await screen.findByText(
        "Searches with profile v4 — the first run authors the search brief."
      )
    ).toBeDefined()
    expect(
      screen.queryByText(/No wants or don't-wants are saved yet/)
    ).toBeNull()
  })

  it("opens and focuses the goal editor from discovery Change goals", async () => {
    stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    // The editor starts closed once explicit choices exist.
    expect(
      await within(wants).findByRole("button", { name: "Edit goals" })
    ).toBeDefined()

    const discovery = await screen.findByRole("region", { name: "Find jobs" })
    fireEvent.click(
      await within(discovery).findByRole("button", { name: "Change goals" })
    )

    const reopened = await screen.findByRole("region", {
      name: "What you want next",
    })
    expect(
      await within(reopened).findByRole("button", { name: "Save goals" })
    ).toBeDefined()
    await waitFor(() =>
      expect(document.activeElement?.getAttribute("id")).toBe("goals-location")
    )
  })

  it("badges unsaved edits, preserves the draft and discards explicitly", async () => {
    stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    const wants = await screen.findByRole("region", {
      name: "What you want next",
    })
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Edit goals" })
    )
    const location = (await within(wants).findByLabelText(
      "Preferred location"
    )) as HTMLInputElement
    fireEvent.change(location, { target: { value: "Rotterdam" } })
    expect(
      await within(wants).findByText("Unsaved changes")
    ).toBeDefined()

    // Closing the editor keeps the draft and stays honest about it.
    fireEvent.click(
      within(wants).getByRole("button", { name: "Close editor" })
    )
    expect(
      await within(wants).findByText(/Unsaved goal changes are waiting/)
    ).toBeDefined()
    fireEvent.click(
      within(wants).getByRole("button", { name: "Edit goals" })
    )
    expect(
      ((await within(wants).findByLabelText(
        "Preferred location"
      )) as HTMLInputElement).value
    ).toBe("Rotterdam")

    // Discarding is explicit and restores the saved values.
    fireEvent.click(
      within(wants).getByRole("button", { name: "Close editor" })
    )
    fireEvent.click(
      await within(wants).findByRole("button", { name: "Discard changes" })
    )
    expect(
      within(wants).queryByText("Unsaved changes")
    ).toBeNull()
    fireEvent.click(
      within(wants).getByRole("button", { name: "Edit goals" })
    )
    expect(
      ((await within(wants).findByLabelText(
        "Preferred location"
      )) as HTMLInputElement).value
    ).toBe("Berlin")
  })
})
