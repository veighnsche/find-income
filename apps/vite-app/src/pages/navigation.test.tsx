// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import App from "@/App"
import { stubFetch } from "@/pages/fixtures"

beforeEach(() => {
  window.location.hash = ""
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.location.hash = ""
})

function go(hash: string) {
  window.location.hash = hash
  window.dispatchEvent(new HashChangeEvent("hashchange"))
}

describe("deep links and history", () => {
  it("opens a job deep link directly on first render", async () => {
    stubFetch()
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(window.location.hash).toBe("#/jobs/job-1")
  })

  it("opens an application deep link directly on first render", async () => {
    stubFetch()
    window.location.hash = "#/applications/job-1"
    render(<App />)

    expect(await screen.findByText("Version 2")).toBeDefined()
  })

  it("follows hash history forward and back without losing the page", async () => {
    stubFetch()
    window.location.hash = "#/jobs"
    render(<App />)
    expect(await screen.findByRole("heading", { name: "Jobs" })).toBeDefined()

    go("#/jobs/job-1")
    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()

    go("#/jobs")
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Jobs" })).toBeDefined()
    )
    expect(window.location.hash).toBe("#/jobs")
  })

  it("navigates through a real anchor click to a job page", async () => {
    stubFetch()
    const user = userEvent.setup()
    window.location.hash = "#/jobs"
    render(<App />)

    await user.click(await screen.findByRole("tab", { name: /Not yet classified/ }))
    const link = await screen.findByRole("link", { name: "Backend Engineer" })
    await user.click(link)

    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
  })
})

describe("review activity disclosure", () => {
  it("toggles with the keyboard and reveals only observed entries", async () => {
    stubFetch()
    const user = userEvent.setup()
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    const trigger = await screen.findByRole("button", { name: /Role review/ })
    expect(trigger.getAttribute("aria-expanded")).toBe("false")

    trigger.focus()
    await user.keyboard("{Enter}")
    await waitFor(() =>
      expect(trigger.getAttribute("aria-expanded")).toBe("true")
    )
    expect(
      await screen.findByText("Owner decision: selected (recorded 2026-09-18)")
    ).toBeDefined()
    expect(
      await screen.findByText(
        "Possible duplicate (same_source_url): Backend Engineer (mirror)"
      )
    ).toBeDefined()

    await user.keyboard(" ")
    await waitFor(() =>
      expect(trigger.getAttribute("aria-expanded")).toBe("false")
    )
  })
})
