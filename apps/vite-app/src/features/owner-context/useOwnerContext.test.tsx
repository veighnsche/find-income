// @vitest-environment jsdom
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import type {
  Preferences,
  Round,
  SearchBriefView,
} from "@/api/client"
import { useSession } from "@/api/session"
import { SessionProvider } from "@/api/session"
import { SavedGoalsProvider } from "@/components/shared/saved-goals"
import { preferencesFixture, sessionFixture } from "@/pages/fixtures"
import {
  buildSavedSummary,
  correctionRequestKey,
  ownerContextPendingKey,
  useOwnerContext,
  type OwnerContextValue,
} from "@/features/owner-context/useOwnerContext"

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

function briefFixture(overrides: Partial<SearchBriefView> = {}): SearchBriefView {
  return {
    profileVersion: 3,
    rubricVersion: "criteria-v3-abc123",
    rubricSource: "preferences_versions:current:v3",
    requirements: [],
    facts: [
      { key: "location", value: "Berlin" },
      { key: "remote", value: "allowed" },
    ],
    ...overrides,
  } as SearchBriefView
}

function roundFixture(overrides: Partial<Round> = {}): Round {
  return {
    id: "round-1",
    requestKey: "profile-correction-v3-00000000",
    state: "queued",
    profileVersion: 3,
    originalProfileVersion: 3,
    ...overrides,
  } as Round
}

function prefsFixture(version: number): Preferences {
  return {
    ...preferencesFixture,
    version,
    preferredLocation: version >= 4 ? "Amsterdam" : preferencesFixture.preferredLocation,
  }
}

interface OwnerFetchScript {
  preferences?: () => Response
  brief?: () => Response
  processInput?: (body: Record<string, unknown>) => Response
  round?: (id: string) => Response
}

function stubOwnerFetch(script: OwnerFetchScript = {}): { calls: FetchCall[] } {
  const calls: FetchCall[] = []
  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const method = (init?.method ?? "GET").toUpperCase()
      const body =
        typeof init?.body === "string" ? (init.body as string) : null
      calls.push({ url, method, body })
      const path = new URL(url, "http://localhost").pathname
      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/preferences")
        return script.preferences === undefined
          ? jsonResponse(200, prefsFixture(3))
          : script.preferences()
      if (path === "/api/v1/research/brief")
        return script.brief === undefined
          ? jsonResponse(200, briefFixture())
          : script.brief()
      if (path === "/api/v1/rounds/process-input" && method === "POST") {
        const parsed = JSON.parse(body ?? "{}") as Record<string, unknown>
        return script.processInput === undefined
          ? jsonResponse(201, { round: roundFixture() })
          : script.processInput(parsed)
      }
      const roundMatch = path.match(/^\/api\/v1\/rounds\/([^/]+)$/)
      if (roundMatch?.[1] !== undefined)
        return script.round === undefined
          ? jsonResponse(200, roundFixture({ id: roundMatch[1] }))
          : script.round(decodeURIComponent(roundMatch[1]))
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function wrapper({ children }: { children: ReactNode }) {
  return (
    <SessionProvider>
      <SavedGoalsProvider>{children}</SavedGoalsProvider>
    </SessionProvider>
  )
}

function useProbe(): { owner: OwnerContextValue; sessionReady: boolean } {
  const owner = useOwnerContext()
  const { session } = useSession()
  return { owner, sessionReady: session !== undefined }
}

async function renderReadyOwner() {
  const rendered = renderHook(() => useProbe(), { wrapper })
  await waitFor(() => {
    expect(rendered.result.current.sessionReady).toBe(true)
    expect(rendered.result.current.owner.contextState).toBe("ready")
  })
  return rendered
}

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe("useOwnerContext saved reads", () => {
  it("exposes the effective saved context without commissioning work", async () => {
    const { calls } = stubOwnerFetch()
    const rendered = await renderReadyOwner()
    const { owner } = rendered.result.current

    expect(owner.context?.profileVersion).toBe(3)
    expect(owner.context?.rubricVersion).toBe("criteria-v3-abc123")
    expect(owner.context?.rubricSource).toBe("preferences_versions:current:v3")
    expect(owner.context?.briefFacts).toHaveLength(2)
    expect(owner.context?.requirements).toHaveLength(2)
    expect(owner.context?.briefStale).toBe(false)
    expect(owner.context?.summary).toBe(
      "Profile v3 · Berlin · remote ok · 2 criteria"
    )
    expect(owner.correction).toEqual({ kind: "idle" })
    expect(owner.discoveryReady).toBe(true)
    expect(owner.discoveryBlockedReason).toBeNull()
    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) expect(call.method).toBe("GET")
  })

  it("treats a missing brief as normal pre-first-run state", async () => {
    stubOwnerFetch({
      brief: () => jsonResponse(404, { error: { message: "No brief." } }),
    })
    const rendered = await renderReadyOwner()
    const { owner } = rendered.result.current

    expect(owner.contextState).toBe("ready")
    expect(owner.context?.rubricVersion).toBeNull()
    expect(owner.context?.briefFacts).toEqual([])
    expect(owner.context?.briefStale).toBe(false)
    expect(owner.discoveryReady).toBe(true)
  })

  it("reports a brief server error as a context error", async () => {
    stubOwnerFetch({
      brief: () => jsonResponse(500, { error: { message: "Brief blew up." } }),
    })
    const rendered = renderHook(() => useProbe(), { wrapper })
    await waitFor(() =>
      expect(rendered.result.current.owner.contextState).toBe("error")
    )
    expect(rendered.result.current.owner.context).toBeNull()
    expect(rendered.result.current.owner.contextError).toBe("Brief blew up.")
    expect(rendered.result.current.owner.discoveryReady).toBe(false)
  })

  it("flags a stale brief and blocks discovery", async () => {
    stubOwnerFetch({
      brief: () =>
        jsonResponse(
          200,
          briefFixture({ profileVersion: 2, rubricVersion: "criteria-v2-old" })
        ),
    })
    const rendered = await renderReadyOwner()
    const { owner } = rendered.result.current

    expect(owner.context?.briefStale).toBe(true)
    expect(owner.discoveryReady).toBe(false)
    expect(owner.discoveryBlockedReason).toMatch(/behind profile version 3/)
  })
})

describe("useOwnerContext correction flow", () => {
  it("keeps accepted work pending until a readback confirms the new version", async () => {
    let serverVersion = 3
    let serverRound = roundFixture({ state: "queued", profileVersion: 3 })
    const { calls } = stubOwnerFetch({
      preferences: () => jsonResponse(200, prefsFixture(serverVersion)),
      brief: () =>
        jsonResponse(
          200,
          briefFixture({
            profileVersion: serverVersion,
            rubricVersion: `criteria-v${serverVersion}-abc123`,
            rubricSource: `preferences_versions:current:v${serverVersion}`,
          })
        ),
      processInput: () => jsonResponse(201, { round: serverRound }),
      round: () => jsonResponse(200, serverRound),
    })
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection(
        "I want remote-only roles."
      )
    })

    const pending = rendered.result.current.owner.correction
    expect(pending.kind).toBe("pending")
    if (pending.kind !== "pending") throw new Error("expected pending")
    expect(pending.baseVersion).toBe(3)
    expect(pending.roundId).toBe("round-1")
    expect(pending.targetText).toBe("I want remote-only roles.")
    expect(rendered.result.current.owner.discoveryReady).toBe(false)
    expect(
      window.localStorage.getItem(ownerContextPendingKey)
    ).not.toBeNull()

    const post = calls.find(
      (call) => call.url.endsWith("/rounds/process-input") && call.method === "POST"
    )
    expect(post).toBeDefined()
    const payload = JSON.parse(post?.body ?? "{}") as Record<string, unknown>
    expect(payload.targetKind).toBe("profile")
    expect(payload.targetId).toBe("current")
    expect(payload.expectedRevision).toBe(3)
    expect(payload.text).toBe("I want remote-only roles.")
    expect(payload.requestKey).toMatch(/^profile-correction-v3-[0-9a-f]{8}$/)

    // The round completes and the profile bumps; only the readback saves it.
    serverRound = roundFixture({ state: "completed", profileVersion: 4 })
    serverVersion = 4
    await act(async () => {
      await rendered.result.current.owner.refreshCorrection()
    })

    const saved = rendered.result.current.owner.correction
    expect(saved).toMatchObject({
      kind: "saved",
      roundId: "round-1",
      savedVersion: 4,
    })
    expect(window.localStorage.getItem(ownerContextPendingKey)).toBeNull()
    await waitFor(() =>
      expect(rendered.result.current.owner.context?.profileVersion).toBe(4)
    )
    expect(rendered.result.current.owner.discoveryReady).toBe(true)
  })

  it("ignores a second submit while a correction is pending", async () => {
    const { calls } = stubOwnerFetch()
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection("first wording")
    })
    await act(async () => {
      await rendered.result.current.owner.submitCorrection("second wording")
    })

    expect(rendered.result.current.owner.correction.kind).toBe("pending")
    expect(
      calls.filter((call) => call.method === "POST")
    ).toHaveLength(1)
  })

  it("reports a stale revision as a conflict with the fresh version", async () => {
    let serverVersion = 3
    stubOwnerFetch({
      preferences: () => jsonResponse(200, prefsFixture(serverVersion)),
      processInput: () => {
        // Another writer lands v4 between our read and our submit.
        serverVersion = 4
        return jsonResponse(409, { error: { message: "Stale revision." } })
      },
    })
    const rendered = await renderReadyOwner()
    await act(async () => {
      await rendered.result.current.owner.submitCorrection("new wording")
    })

    const correction = rendered.result.current.owner.correction
    expect(correction.kind).toBe("conflict")
    if (correction.kind !== "conflict") throw new Error("expected conflict")
    expect(correction.baseVersion).toBe(3)
    expect(correction.currentVersion).toBe(4)
    expect(correction.message).toMatch(/now version 4/)
    expect(rendered.result.current.owner.discoveryReady).toBe(false)
    await waitFor(() =>
      expect(rendered.result.current.owner.context?.profileVersion).toBe(4)
    )
  })

  it("reports a same-version 409 without claiming the profile moved", async () => {
    stubOwnerFetch({
      processInput: () =>
        jsonResponse(409, { error: { message: "Changed input." } }),
    })
    const rendered = await renderReadyOwner()
    await act(async () => {
      await rendered.result.current.owner.submitCorrection("repeat wording")
    })

    const correction = rendered.result.current.owner.correction
    expect(correction.kind).toBe("conflict")
    if (correction.kind !== "conflict") throw new Error("expected conflict")
    expect(correction.baseVersion).toBe(3)
    expect(correction.currentVersion).toBe(3)
    expect(correction.message).toMatch(/already be in flight/)
  })

  it("reports a failed round as a retryable error, never saved", async () => {
    stubOwnerFetch({
      processInput: () =>
        jsonResponse(201, {
          round: roundFixture({ state: "failed", profileVersion: 3 }),
        }),
    })
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection("doomed wording")
    })

    const correction = rendered.result.current.owner.correction
    expect(correction.kind).toBe("error")
    if (correction.kind !== "error") throw new Error("expected error")
    expect(correction.retryable).toBe(true)
    expect(correction.message).toMatch(/failed before saving/)
  })

  it("reports a completed-but-unchanged round as not saved", async () => {
    stubOwnerFetch({
      processInput: () =>
        jsonResponse(201, {
          round: roundFixture({ state: "completed", profileVersion: 3 }),
        }),
    })
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection("no-op wording")
    })

    const correction = rendered.result.current.owner.correction
    expect(correction.kind).toBe("error")
    if (correction.kind !== "error") throw new Error("expected error")
    expect(correction.retryable).toBe(false)
    expect(correction.message).toMatch(/still version 3/)
  })

  it("reports a version bump owned by someone else as a conflict", async () => {
    let serverVersion = 3
    let serverRound = roundFixture({ state: "queued", profileVersion: 3 })
    stubOwnerFetch({
      preferences: () => jsonResponse(200, prefsFixture(serverVersion)),
      processInput: () => jsonResponse(201, { round: serverRound }),
      round: () => jsonResponse(200, serverRound),
    })
    const rendered = await renderReadyOwner()
    await act(async () => {
      await rendered.result.current.owner.submitCorrection("late wording")
    })
    // Our round saved v4, but another writer already moved the profile to v5.
    serverVersion = 5
    serverRound = roundFixture({ state: "completed", profileVersion: 4 })
    await act(async () => {
      await rendered.result.current.owner.refreshCorrection()
    })

    const correction = rendered.result.current.owner.correction
    expect(correction.kind).toBe("conflict")
    if (correction.kind !== "conflict") throw new Error("expected conflict")
    expect(correction.baseVersion).toBe(3)
    expect(correction.currentVersion).toBe(5)
  })

  it("maps runner-not-ready and invalid input to honest errors", async () => {
    stubOwnerFetch({
      processInput: () =>
        jsonResponse(503, { error: { message: "Runner not ready." } }),
    })
    const rendered = await renderReadyOwner()
    await act(async () => {
      await rendered.result.current.owner.submitCorrection("wording")
    })
    const unavailable = rendered.result.current.owner.correction
    expect(unavailable.kind).toBe("error")
    if (unavailable.kind !== "error") throw new Error("expected error")
    expect(unavailable.retryable).toBe(true)
    expect(unavailable.message).toMatch(/not ready/)
  })

  it("keeps a paused round pending with its paused state visible", async () => {
    let serverRound = roundFixture({ state: "paused", profileVersion: 3 })
    stubOwnerFetch({
      processInput: () => jsonResponse(201, { round: serverRound }),
      round: () => jsonResponse(200, serverRound),
    })
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection("patient wording")
    })

    const pending = rendered.result.current.owner.correction
    expect(pending.kind).toBe("pending")
    if (pending.kind !== "pending") throw new Error("expected pending")
    expect(pending.roundState).toBe("paused")
    expect(rendered.result.current.owner.discoveryBlockedReason).toMatch(
      /paused/
    )

    serverRound = roundFixture({ state: "running", profileVersion: 3 })
    await act(async () => {
      await rendered.result.current.owner.refreshCorrection()
    })
    const resumed = rendered.result.current.owner.correction
    expect(resumed.kind).toBe("pending")
    if (resumed.kind !== "pending") throw new Error("expected pending")
    expect(resumed.roundState).toBe("running")
  })

  it("reports an unknown round instead of tracking forever", async () => {
    stubOwnerFetch({
      round: () => jsonResponse(404, { error: { message: "No round." } }),
    })
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection("lost wording")
    })
    await act(async () => {
      await rendered.result.current.owner.refreshCorrection()
    })

    const correction = rendered.result.current.owner.correction
    expect(correction.kind).toBe("error")
    if (correction.kind !== "error") throw new Error("expected error")
    expect(correction.message).toMatch(/no longer known/)
  })

  it("resumes a persisted pending correction after reload", async () => {
    window.localStorage.setItem(
      ownerContextPendingKey,
      JSON.stringify({
        roundId: "round-9",
        requestKey: "profile-correction-v3-deadbeef",
        baseVersion: 3,
        targetText: "reloaded wording",
      })
    )
    const { calls } = stubOwnerFetch({
      preferences: () => jsonResponse(200, prefsFixture(4)),
      round: (id) =>
        jsonResponse(
          200,
          roundFixture({ id, state: "completed", profileVersion: 4 })
        ),
    })
    const rendered = renderHook(() => useProbe(), { wrapper })

    await waitFor(() =>
      expect(rendered.result.current.owner.correction.kind).toBe("saved")
    )
    expect(
      calls.some(
        (call) => call.method === "GET" && call.url.endsWith("/rounds/round-9")
      )
    ).toBe(true)
    for (const call of calls) expect(call.method).toBe("GET")
  })

  it("dismisses terminal states but never a pending correction", async () => {
    stubOwnerFetch({
      processInput: () =>
        jsonResponse(201, {
          round: roundFixture({ state: "failed", profileVersion: 3 }),
        }),
    })
    const rendered = await renderReadyOwner()

    await act(async () => {
      await rendered.result.current.owner.submitCorrection("failing wording")
    })
    expect(rendered.result.current.owner.correction.kind).toBe("error")
    act(() => {
      rendered.result.current.owner.dismissCorrection()
    })
    expect(rendered.result.current.owner.correction).toEqual({ kind: "idle" })
    expect(rendered.result.current.owner.discoveryReady).toBe(true)
  })
})

describe("owner-context pure helpers", () => {
  it("builds stable request keys per distinct wording", () => {
    expect(correctionRequestKey(3, "want remote")).toBe(
      correctionRequestKey(3, "  want   remote ")
    )
    expect(correctionRequestKey(3, "want remote")).not.toBe(
      correctionRequestKey(3, "want onsite")
    )
    expect(correctionRequestKey(3, "want remote")).not.toBe(
      correctionRequestKey(4, "want remote")
    )
    expect(correctionRequestKey(3, "want remote")).toMatch(
      /^profile-correction-v3-[0-9a-f]{8}$/
    )
  })

  it("maps the remote/hybrid booleans onto summary wording", () => {
    expect(buildSavedSummary(preferencesFixture)).toMatch(/remote ok/)
    expect(
      buildSavedSummary({ ...preferencesFixture, allowRemote: false })
    ).toMatch(/onsite only/)
    expect(
      buildSavedSummary({
        ...preferencesFixture,
        allowRemote: false,
        allowHybrid: true,
      })
    ).toMatch(/hybrid ok/)
    expect(buildSavedSummary({ ...preferencesFixture, roleCriteria: [] })).toMatch(
      /0 criteria/
    )
  })
})
