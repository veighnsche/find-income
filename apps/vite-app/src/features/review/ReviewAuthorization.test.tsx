// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { SessionProvider } from "@/api/session"
import { sessionFixture } from "@/pages/fixtures"
import { ReviewAuthorization } from "@/features/review/ReviewAuthorization"
import type { DeliveryReview } from "@/api/client"

interface FetchCall {
  url: string
  method: string
  body: string | null
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function reviewFixture(overrides: Partial<DeliveryReview> = {}): DeliveryReview {
  return {
    id: "review-1",
    materialSha256: "a".repeat(64),
    items: [
      {
        id: "item-1",
        packId: "pack-7",
        opportunityId: "job-1",
        opportunityRevision: 2,
        profileRevision: 3,
        packContentSha256: "b".repeat(64),
        recipient: "jobs@harbour.example",
        current: true,
        attachmentSha256: "c".repeat(64),
        body: "I would like to apply.",
        companyName: "Harbour",
        messageId: "<review-1@local>",
        mimeSha256: "d".repeat(64),
        title: "Backend Engineer",
      },
    ],
    ...overrides,
  } as DeliveryReview
}

function stubFetch(options: { prepareStatus?: number; approveStatus?: number } = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url =
      typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ url, method, body: typeof init?.body === "string" ? init.body : null })
    const path = new URL(url, "http://localhost").pathname
    if (path === "/api/v1/auth/session") return jsonResponse(200, sessionFixture)
    if (path === "/api/v1/delivery/reviews" && method === "POST") {
      const status = options.prepareStatus ?? 201
      if (status !== 201) return jsonResponse(status, { error: { message: "refused" } })
      return jsonResponse(201, reviewFixture())
    }
    const approve = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/approve$/)
    if (approve?.[1] !== undefined && method === "POST") {
      const status = options.approveStatus ?? 200
      if (status !== 200) return jsonResponse(status, { error: { message: "refused" } })
      return jsonResponse(
        200,
        reviewFixture({ approvedAt: "2026-09-25T12:00:00Z", approvedSha256: "a".repeat(64) })
      )
    }
    return jsonResponse(404, { error: { message: "Not found." } })
  })
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderAuthorization(props: { packId: string | null; materialReady: boolean; materialVersion: number | null }) {
  return render(
    <SessionProvider>
      <ReviewAuthorization {...props} />
    </SessionProvider>
  )
}

it("mounts without commissioning review work", async () => {
  const { calls } = stubFetch()
  renderAuthorization({ packId: "pack-7", materialReady: true, materialVersion: 2 })
  expect(await screen.findByRole("button", { name: "Prepare review" })).toBeDefined()
  expect(calls.some((call) => call.method === "POST")).toBe(false)
})

it("prepares then approves exactly the bound pack and digest", async () => {
  const user = userEvent.setup()
  const { calls } = stubFetch()
  renderAuthorization({ packId: "pack-7", materialReady: true, materialVersion: 2 })
  await user.click(await screen.findByRole("button", { name: "Prepare review" }))
  expect(await screen.findByRole("button", { name: "Approve this review" })).toBeDefined()
  const prepare = calls.find(
    (call) => call.url.endsWith("/delivery/reviews") && call.method === "POST"
  )
  expect(prepare).toBeDefined()
  expect(JSON.parse(prepare?.body ?? "{}")).toEqual({
    requestKey: expect.any(String),
    packIds: ["pack-7"],
  })
  await user.click(screen.getByRole("button", { name: "Approve this review" }))
  expect(await screen.findByText("Review approved")).toBeDefined()
  const approve = calls.find((call) => call.url.endsWith("/approve") && call.method === "POST")
  expect(JSON.parse(approve?.body ?? "{}")).toEqual({ materialSha256: "a".repeat(64) })
  expect(calls.some((call) => call.url.endsWith("/send"))).toBe(false)
})

it("reports a prepare conflict as changed material", async () => {
  const user = userEvent.setup()
  stubFetch({ prepareStatus: 409 })
  renderAuthorization({ packId: "pack-7", materialReady: true, materialVersion: 2 })
  await user.click(await screen.findByRole("button", { name: "Prepare review" }))
  expect(await screen.findByText(/The material changed/)).toBeDefined()
})

it("reports an approve conflict as a stale review", async () => {
  const user = userEvent.setup()
  stubFetch({ approveStatus: 409 })
  renderAuthorization({ packId: "pack-7", materialReady: true, materialVersion: 2 })
  await user.click(await screen.findByRole("button", { name: "Prepare review" }))
  await user.click(await screen.findByRole("button", { name: "Approve this review" }))
  expect(await screen.findByText(/changed after this review was prepared/)).toBeDefined()
})

it("blocks authorization without a pack or with unready material", async () => {
  stubFetch()
  const first = renderAuthorization({ packId: null, materialReady: false, materialVersion: null })
  expect(await screen.findByText("Nothing to authorize yet")).toBeDefined()
  first.unmount()
  renderAuthorization({ packId: "pack-7", materialReady: false, materialVersion: 2 })
  await waitFor(() =>
    expect(screen.queryByText("Materials not ready")).not.toBeNull()
  )
})
