// @vitest-environment jsdom
import { useEffect } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { SessionProvider } from "@/api/session"
import {
  notifyAccepted,
  scopeVersion,
  useInvalidate,
  type InvalidationScope,
} from "@/components/shared/invalidation"
import { stubFetch } from "@/pages/fixtures"
import { useRead } from "@/pages/useRead"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function Probe({
  label,
  url,
  scopes,
}: {
  label: string
  url: string
  scopes?: InvalidationScope[]
}) {
  const read = useRead(
    `invalidation-probe:${label}`,
    (signal) =>
      fetch(url, { signal }).then((response) => response.json()) as Promise<{
        version?: number
      }>,
    scopes === undefined ? undefined : { scopes }
  )
  return (
    <p>
      {label}: {read.status}
    </p>
  )
}

describe("scoped invalidation", () => {
  it("bumps only the notified scopes", () => {
    const before = scopeVersion("answers")
    notifyAccepted("answers")
    expect(scopeVersion("answers")).toBe(before + 1)
    const materialsBefore = scopeVersion("materials")
    notifyAccepted("answers")
    expect(scopeVersion("materials")).toBe(materialsBefore)
  })

  it("refetches subscribed reads only, without blind global polling", async () => {
    const { calls } = stubFetch()
    render(
      <SessionProvider>
        <Probe label="goals" url="/api/v1/preferences" scopes={["goals"]} />
        <Probe label="plain" url="/api/v1/runtime-status" />
      </SessionProvider>
    )
    await screen.findByText("goals: ready")
    await screen.findByText("plain: ready")
    const preferencesCalls = () =>
      calls.filter(
        (call) =>
          call.url === "/api/v1/preferences" && call.method === "GET"
      ).length
    const runtimeCalls = () =>
      calls.filter(
        (call) =>
          call.url === "/api/v1/runtime-status" && call.method === "GET"
      ).length
    expect(preferencesCalls()).toBe(1)
    expect(runtimeCalls()).toBe(1)

    act(() => {
      notifyAccepted("goals")
    })

    await waitFor(() => expect(preferencesCalls()).toBe(2))
    await waitFor(() => expect(runtimeCalls()).toBe(1))
    expect(
      calls.every(
        (call) =>
          call.url === "/api/v1/auth/session" || call.method === "GET"
      )
    ).toBe(true)
  })

  it("exposes a stable notifier for write handlers", async () => {
    stubFetch()
    let invalidate: ((...scopes: ["check"]) => void) | null = null
    function Capture() {
      const value = useInvalidate()
      useEffect(() => {
        invalidate = value
      }, [value])
      return null
    }
    render(
      <SessionProvider>
        <Capture />
      </SessionProvider>
    )
    const before = scopeVersion("check")
    act(() => {
      invalidate?.("check")
    })
    expect(scopeVersion("check")).toBe(before + 1)
  })
})
