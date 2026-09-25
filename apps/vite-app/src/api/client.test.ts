import { afterEach, describe, expect, it, vi } from "vitest"
import {
  getHealth,
  getSession,
  isUnauthenticated,
  login,
  logout,
  RequestError,
  type Session,
} from "./client"
import { sessionExpired } from "./session"

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const session: Session = {
  actorKind: "administrator",
  actorId: "owner",
  csrfToken: "csrf-token",
  expiresAt: "2099-01-01T00:00:00Z",
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("session restore", () => {
  it("returns the session when authenticated", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, session))
    vi.stubGlobal("fetch", fetch)
    await expect(getSession()).resolves.toEqual(session)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/auth/session",
      expect.objectContaining({ credentials: "same-origin" })
    )
  })

  it("returns null on expired session (401)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(401, {})))
    await expect(getSession()).resolves.toBeNull()
  })

  it("throws a RequestError when the session read fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(500, {})))
    const cause = await getSession().catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(500)
  })
})

describe("authentication", () => {
  it("signs in with the password payload", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, session))
    vi.stubGlobal("fetch", fetch)
    await expect(login("secret")).resolves.toEqual(session)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/auth/login",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ password: "secret" }),
      })
    )
  })

  it("surfaces the server message on invalid credentials", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(401, { error: { message: "Invalid credentials." } }))
    )
    const cause = await login("wrong").catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(401)
    expect((cause as RequestError).message).toBe("Invalid credentials.")
    expect(isUnauthenticated(cause)).toBe(true)
  })

  it("signs out with the CSRF token", async () => {
    const fetch = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetch)
    await expect(logout("csrf-token")).resolves.toBeUndefined()
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/auth/logout",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      })
    )
  })
})

describe("failed requests", () => {
  it("keeps envelope details on errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(409, { error: { message: "Conflict.", details: { rev: 2 } } })
      )
    )
    const cause = await login("x").catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).message).toBe("Conflict.")
    expect((cause as RequestError).details).toEqual({ rev: 2 })
    expect(isUnauthenticated(cause)).toBe(false)
  })

  it("falls back when the server returns no JSON envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("<html>down</html>", { status: 502 }))
    )
    const cause = await login("x").catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(502)
    expect((cause as RequestError).message).toContain("502")
  })

  it("rejects unexpected health responses", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(200, { status: "ok", service: "other", version: "1" }))
    )
    await expect(getHealth()).rejects.toThrow("unexpected health response")
  })
})

describe("session expiry", () => {
  it("detects expired and live sessions", () => {
    expect(sessionExpired(session, Date.parse("2099-01-02T00:00:00Z"))).toBe(true)
    expect(sessionExpired(session, Date.parse("2026-01-01T00:00:00Z"))).toBe(false)
  })
})
