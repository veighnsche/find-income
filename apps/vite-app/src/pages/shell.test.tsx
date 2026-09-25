// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
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

describe("read-only shell", () => {
  it("renders Today from real preference, opportunity and runtime reads", async () => {
    stubFetch()
    render(<App />)

    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()
    expect(await screen.findByText("1 active of 2 tracked roles.")).toBeDefined()
    expect(
      await screen.findByText(/Search profile version 3 · Berlin/)
    ).toBeDefined()
    const nav = screen.getByRole("navigation", { name: "Primary" })
    expect(nav.querySelector('a[href="#/jobs"]')).not.toBeNull()
  })

  it("renders My search with the saved criteria", async () => {
    stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "My search" })
    ).toBeDefined()
    expect(await screen.findByText("Profile version 3")).toBeDefined()
    expect(await screen.findByText("Backend engineer")).toBeDefined()
    expect(await screen.findByText("role · require")).toBeDefined()
    expect(await screen.findByText("EUR 4500.00")).toBeDefined()
  })

  it("renders grouped Jobs with deep links", async () => {
    stubFetch()
    window.location.hash = "#/jobs"
    render(<App />)

    const link = (await screen.findByRole("link", {
      name: "Backend Engineer",
    })) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe("#/jobs/job-1")
    expect(await screen.findByText(/Not yet classified \(2\)/)).toBeDefined()
  })

  it("renders a job detail with decision, pay and live stage", async () => {
    stubFetch()
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(
      await screen.findByText("EUR 4000.00 – EUR 6000.00, per month, base")
    ).toBeDefined()
    expect(await screen.findByText("selected")).toBeDefined()
    expect(
      await screen.findByText("Current stage: Select jobs (selected)")
    ).toBeDefined()
    await waitFor(() => {
      const current = document.querySelector('li[aria-current="step"]')
      expect(current?.textContent).toContain("Select jobs")
    })
    expect(
      screen.queryByText("Per-role stage actions are not available yet")
    ).toBeNull()
  })

  it("renders saved application packs per role", async () => {
    stubFetch()
    window.location.hash = "#/applications/job-1"
    render(<App />)

    expect(await screen.findByText("Version 1")).toBeDefined()
    expect(await screen.findByText("Version 2")).toBeDefined()
    expect(await screen.findByText(/2 packs saved/)).toBeDefined()
  })

  it("shows the server message when a job does not exist", async () => {
    stubFetch()
    window.location.hash = "#/jobs/missing"
    render(<App />)

    expect(
      await screen.findByText("Opportunity not found.")
    ).toBeDefined()
  })

  it("shows the sign-in panel when the session is null and signs in", async () => {
    stubFetch({ session: null })
    render(<App />)

    const password = (await screen.findByLabelText(
      "Password"
    )) as HTMLInputElement
    fireEvent.change(password, { target: { value: "secret" } })
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))

    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()
  })

  it("commissions no work while reading and navigating every surface", async () => {
    const { calls } = stubFetch()
    render(<App />)
    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()

    const go = (hash: string) => {
      window.location.hash = hash
      window.dispatchEvent(new HashChangeEvent("hashchange"))
    }

    go("#/search")
    expect(
      await screen.findByRole("heading", { name: "My search" })
    ).toBeDefined()
    go("#/jobs")
    expect(await screen.findByRole("heading", { name: "Jobs" })).toBeDefined()
    go("#/jobs/job-1")
    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(await screen.findByText("selected")).toBeDefined()
    go("#/applications")
    expect(
      await screen.findByRole("heading", { name: "Applications" })
    ).toBeDefined()
    go("#/applications/job-1")
    expect(await screen.findByText("Version 2")).toBeDefined()
    go("#/today")
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Today" })).toBeDefined()
    )

    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(call.method).toBe("GET")
      expect(call.url).not.toMatch(
        /\/commission|\/prepare|\/process-input|\/connect/
      )
    }
    const posts = calls.filter((call) => call.method === "POST")
    expect(posts).toEqual([])
  })
})
