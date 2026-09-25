// @vitest-environment jsdom
import { useState } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { SessionProvider } from "@/api/session"
import { sessionFixture } from "@/pages/fixtures"
import { SendReview, sendOutcomeText } from "@/features/review/SendReview"
import { listAttemptReviews } from "@/features/attempts"
import type {
  DeliveryItem,
  DeliveryReconciliation,
  DeliveryReview,
  Round,
} from "@/api/client"

interface FetchCall {
  url: string
  method: string
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function itemFixture(overrides: Partial<DeliveryItem> = {}): DeliveryItem {
  return {
    id: "item-1",
    reviewId: "review-1",
    packId: "pack-7",
    opportunityId: "job-1",
    opportunityRevision: 2,
    profileRevision: 3,
    packContentSha256: "b".repeat(64),
    routeId: "route-1",
    routeRevision: 1,
    routeSha256: "e".repeat(64),
    title: "Backend Engineer",
    companyName: "Harbour",
    routeExcerpt: "jobs@harbour.example",
    recipient: "jobs@harbour.example",
    sender: "owner@example.invalid",
    subject: "Application",
    body: "I would like to apply.",
    attachmentSha256: "c".repeat(64),
    mimeSha256: "d".repeat(64),
    messageId: "<review-1@local>",
    state: "prepared",
    current: true,
    ...overrides,
  } as DeliveryItem
}

function reviewFixture(overrides: Partial<DeliveryReview> = {}): DeliveryReview {
  return {
    id: "review-1",
    materialSha256: "a".repeat(64),
    approvedAt: "2026-09-25T12:00:00Z",
    approvedSha256: "a".repeat(64),
    items: [itemFixture()],
    ...overrides,
  } as DeliveryReview
}

function roundFixture(overrides: Partial<Round> = {}): Round {
  return {
    id: "round-1",
    requestKey: "delivery:review-1",
    state: "completed",
    ...overrides,
  } as Round
}

interface StubHooks {
  onSend?: (url: string) => Promise<Response>
  latestReview?: () => DeliveryReview
  reconcile?: () => DeliveryReconciliation
}

function stubFetch(hooks: StubHooks = {}): { calls: FetchCall[] } {
  const calls: FetchCall[] = []
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url =
      typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ url, method })
    const path = new URL(url, "http://localhost").pathname
    if (path === "/api/v1/auth/session") return jsonResponse(200, sessionFixture)
    const send = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/send$/)
    if (send?.[1] !== undefined && method === "POST") {
      if (hooks.onSend !== undefined) return hooks.onSend(url)
      return jsonResponse(200, { review: reviewFixture(), round: roundFixture() })
    }
    const reconcile = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/reconcile$/)
    if (reconcile?.[1] !== undefined && method === "POST") {
      const result =
        hooks.reconcile?.() ?? { supported: false, reason: "adapter cannot verify" }
      return jsonResponse(200, result)
    }
    const read = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)$/)
    if (read?.[1] !== undefined && method === "GET") {
      return jsonResponse(200, hooks.latestReview?.() ?? reviewFixture())
    }
    return jsonResponse(404, { error: { message: "Not found." } })
  })
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

function renderSend(initial: DeliveryReview, jobId = "job-1") {
  function Harness() {
    const [review, setReview] = useState(initial)
    return <SendReview jobId={jobId} review={review} onReviewChange={setReview} />
  }
  return render(
    <SessionProvider>
      <Harness />
    </SessionProvider>
  )
}

function sendCalls(calls: FetchCall[]): FetchCall[] {
  return calls.filter((call) => call.url.endsWith("/send") && call.method === "POST")
}

it("mounts without sending and renders per-item outcomes", async () => {
  const { calls } = stubFetch()
  renderSend(reviewFixture())
  expect(await screen.findByRole("button", { name: "Send approved review" })).toBeDefined()
  expect(screen.getByText("Prepared; no submission attempted.")).toBeDefined()
  expect(calls.some((call) => call.method === "POST")).toBe(false)
})

it("duplicate clicks POST the same send exactly once", async () => {
  let release!: (response: Response) => void
  const gate = new Promise<Response>((resolve) => {
    release = resolve
  })
  const sent = reviewFixture({
    items: [
      itemFixture({ state: "accepted_by_smtp", smtpStage: "data_reply", smtpCode: 250 }),
    ],
  })
  const { calls } = stubFetch({ onSend: () => gate })
  renderSend(reviewFixture())
  const button = (await screen.findByRole("button", {
    name: "Send approved review",
  })) as HTMLButtonElement
  fireEvent.click(button)
  fireEvent.click(button)
  fireEvent.click(button)
  expect(sendCalls(calls)).toHaveLength(1)
  release(jsonResponse(200, { review: sent, round: roundFixture() }))
  expect(await screen.findByText(/Accepted by the mail server/)).toBeDefined()
  expect(sendCalls(calls)).toHaveLength(1)
  expect(sendCalls(calls)[0]?.url).toContain("/delivery/reviews/review-1/send")
})

it("describes SMTP acceptance without claiming confirmed receipt", async () => {
  const sent = reviewFixture({
    items: [
      itemFixture({ state: "accepted_by_smtp", smtpStage: "data_reply", smtpCode: 250 }),
    ],
  })
  stubFetch({
    onSend: async () => jsonResponse(200, { review: sent, round: roundFixture() }),
  })
  const user = userEvent.setup()
  renderSend(reviewFixture())
  await user.click(await screen.findByRole("button", { name: "Send approved review" }))
  expect(
    await screen.findByText("Accepted by the mail server, not confirmed receipt.")
  ).toBeDefined()
  expect(document.body.textContent ?? "").not.toMatch(/delivered|received/i)
  expect(sendOutcomeText("accepted_by_smtp")).not.toMatch(/delivered|received/i)
})

it("keeps partial per-item outcomes visible", async () => {
  const sent = reviewFixture({
    items: [
      itemFixture({ id: "item-1", state: "accepted_by_smtp" }),
      itemFixture({ id: "item-2", state: "failed", outcomeDetail: "connection refused" }),
      itemFixture({ id: "item-3", state: "uncertain", smtpStage: "data_write" }),
    ],
  })
  stubFetch({
    onSend: async () => jsonResponse(200, { review: sent, round: roundFixture() }),
  })
  const user = userEvent.setup()
  renderSend(
    reviewFixture({ items: [itemFixture({ id: "item-1" }), itemFixture({ id: "item-2" }), itemFixture({ id: "item-3" })] })
  )
  await user.click(await screen.findByRole("button", { name: "Send approved review" }))
  expect(
    await screen.findByText("Accepted by the mail server, not confirmed receipt.")
  ).toBeDefined()
  expect(screen.getByText("Submission failed.")).toBeDefined()
  expect(
    screen.getByText(
      "Submission outcome unknown. It may have been accepted; do not send again."
    )
  ).toBeDefined()
  expect(screen.getByText("connection refused")).toBeDefined()
  expect(screen.queryByRole("button", { name: "Send approved review" })).toBeNull()
  expect(document.body.textContent ?? "").not.toMatch(/delivered|received/i)
})

it("reports a definitive refusal without retrying", async () => {
  const { calls } = stubFetch({
    onSend: async () => jsonResponse(503, { error: { message: "sender unavailable" } }),
  })
  const user = userEvent.setup()
  renderSend(reviewFixture())
  await user.click(await screen.findByRole("button", { name: "Send approved review" }))
  expect(await screen.findByText(/Nothing was sent/)).toBeDefined()
  expect(sendCalls(calls)).toHaveLength(1)
  expect(calls.some((call) => call.url.endsWith("/reconcile"))).toBe(false)
})

it("requires a status read before replaying after a lost response", async () => {
  let attempts = 0
  const { calls } = stubFetch({
    onSend: async () => {
      attempts += 1
      if (attempts === 1) throw new TypeError("network down")
      const sent = reviewFixture({
        items: [itemFixture({ state: "accepted_by_smtp" })],
      })
      return jsonResponse(200, { review: sent, round: roundFixture() })
    },
  })
  const user = userEvent.setup()
  renderSend(reviewFixture())
  await user.click(await screen.findByRole("button", { name: "Send approved review" }))
  expect(await screen.findByText(/its outcome is uncertain/)).toBeDefined()
  expect(screen.queryByRole("button", { name: "Send approved review" })).toBeNull()
  const readButton = await screen.findByRole("button", { name: "Read current status" })
  await user.click(readButton)
  expect(await screen.findByText(/This read never sends/)).toBeDefined()
  const replay = (await screen.findByRole("button", {
    name: "Send approved review",
  })) as HTMLButtonElement
  expect(replay.disabled).toBe(false)
  await user.click(replay)
  expect(
    await screen.findByText("Accepted by the mail server, not confirmed receipt.")
  ).toBeDefined()
  const sends = sendCalls(calls)
  expect(sends).toHaveLength(2)
  expect(sends[0]?.url).toBe(sends[1]?.url)
  expect(calls.filter((call) => call.method === "GET" && call.url.includes("/delivery/reviews/"))).toHaveLength(1)
})

it("treats a send conflict as unknown until the status is read", async () => {
  const { calls } = stubFetch({
    onSend: async () => jsonResponse(409, { error: { message: "conflict" } }),
    latestReview: () =>
      reviewFixture({ items: [itemFixture({ state: "accepted_by_smtp" })] }),
  })
  const user = userEvent.setup()
  renderSend(reviewFixture())
  await user.click(await screen.findByRole("button", { name: "Send approved review" }))
  expect(await screen.findByText(/may already be in progress/)).toBeDefined()
  expect(screen.queryByRole("button", { name: "Send approved review" })).toBeNull()
  await user.click(await screen.findByRole("button", { name: "Read current status" }))
  expect(
    await screen.findByText("Accepted by the mail server, not confirmed receipt.")
  ).toBeDefined()
  expect(sendCalls(calls)).toHaveLength(1)
})

it("reconciles uncertainty with reads only and never resends", async () => {
  const uncertain = reviewFixture({
    items: [
      itemFixture({ id: "item-1", state: "accepted_by_smtp" }),
      itemFixture({ id: "item-2", state: "uncertain", smtpStage: "data_write" }),
    ],
  })
  const { calls } = stubFetch({
    latestReview: () => uncertain,
    reconcile: () => ({
      supported: false,
      reason: "This SMTP adapter cannot verify employer receipt.",
    }),
  })
  const user = userEvent.setup()
  renderSend(uncertain)
  expect(await screen.findByRole("button", { name: "Reconcile uncertain outcome" })).toBeDefined()
  expect(screen.getByText(/without resending/)).toBeDefined()
  await user.click(screen.getByRole("button", { name: "Reconcile uncertain outcome" }))
  expect(await screen.findByText(/Read-only receipt check unsupported/)).toBeDefined()
  expect(sendCalls(calls)).toHaveLength(0)
  expect(
    calls.filter((call) => call.url.endsWith("/reconcile") && call.method === "POST")
  ).toHaveLength(1)
  expect(
    calls.filter((call) => call.method === "GET" && call.url.includes("/delivery/reviews/"))
  ).toHaveLength(1)
  expect(document.body.textContent ?? "").not.toMatch(/delivered|received/i)
})

it("shows no reconcile control when nothing is uncertain", async () => {
  stubFetch()
  renderSend(
    reviewFixture({ items: [itemFixture({ state: "accepted_by_smtp" })] })
  )
  expect(await screen.findByText(/Accepted by the mail server/)).toBeDefined()
  expect(screen.queryByRole("button", { name: "Reconcile uncertain outcome" })).toBeNull()
})

it("records the attempt index and links the outcome view after a send", async () => {
  const sent = reviewFixture({
    items: [itemFixture({ state: "accepted_by_smtp" })],
  })
  stubFetch({
    onSend: async () => jsonResponse(200, { review: sent, round: roundFixture() }),
  })
  const user = userEvent.setup()
  renderSend(reviewFixture(), "job-9")
  await user.click(await screen.findByRole("button", { name: "Send approved review" }))
  const link = await screen.findByRole("link", { name: "View attempt outcome" })
  expect(link.getAttribute("href")).toBe("#/applications/job-9/attempts/review-1")
  expect(listAttemptReviews("job-9")).toContain("review-1")
})
