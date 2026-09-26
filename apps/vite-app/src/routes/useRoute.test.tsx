import { describe, expect, it } from "vitest"
import { parseHash, routeToHash, type Route } from "@/routes/useRoute"

describe("parseHash", () => {
  it("treats an empty hash as Today so refresh keeps a default page", () => {
    expect(parseHash("")).toEqual({ page: "today" })
    expect(parseHash("#/")).toEqual({ page: "today" })
    expect(parseHash("#/today")).toEqual({ page: "today" })
  })

  it("parses the four read-only surfaces", () => {
    expect(parseHash("#/search")).toEqual({ page: "search" })
    expect(parseHash("#/jobs")).toEqual({ page: "jobs", jobId: null })
    expect(parseHash("#/applications")).toEqual({
      page: "applications",
      jobId: null,
    })
  })

  it("parses job-specific deep links", () => {
    expect(parseHash("#/jobs/job-1")).toEqual({ page: "jobs", jobId: "job-1" })
    expect(parseHash("#/applications/job-2")).toEqual({
      page: "applications",
      jobId: "job-2",
    })
    expect(parseHash("#/jobs/job-1/check")).toEqual({
      page: "check",
      jobId: "job-1",
    })
    expect(parseHash("#/jobs/job-1/answers")).toEqual({
      page: "answers",
      jobId: "job-1",
    })
    expect(parseHash("#/jobs/job-1/prepare")).toEqual({
      page: "prepare",
      jobId: "job-1",
    })
    expect(parseHash("#/jobs/job-1/handoff")).toEqual({
      page: "handoff",
      jobId: "job-1",
    })
  })

  it("decodes and round-trips identifiers with special characters", () => {
    const route: Route = { page: "jobs", jobId: "job 1/2" }
    expect(parseHash(routeToHash(route))).toEqual(route)
  })

  it("ignores query strings and reports unknown pages honestly", () => {
    expect(parseHash("#/jobs?utm=x")).toEqual({ page: "jobs", jobId: null })
    expect(parseHash("#/unknown")).toEqual({
      page: "not-found",
      hash: "#/unknown",
    })
    expect(parseHash("#/jobs/a/b")).toEqual({
      page: "not-found",
      hash: "#/jobs/a/b",
    })
  })
})

describe("routeToHash", () => {
  it("serializes every route back to a bookmarkable hash", () => {
    expect(routeToHash({ page: "today" })).toBe("#/today")
    expect(routeToHash({ page: "search" })).toBe("#/search")
    expect(routeToHash({ page: "jobs", jobId: null })).toBe("#/jobs")
    expect(routeToHash({ page: "jobs", jobId: "job-1" })).toBe("#/jobs/job-1")
    expect(routeToHash({ page: "applications", jobId: null })).toBe(
      "#/applications"
    )
    expect(routeToHash({ page: "applications", jobId: "job-2" })).toBe(
      "#/applications/job-2"
    )
    expect(routeToHash({ page: "check", jobId: "job-1" })).toBe(
      "#/jobs/job-1/check"
    )
    expect(routeToHash({ page: "answers", jobId: "job-1" })).toBe(
      "#/jobs/job-1/answers"
    )
    expect(routeToHash({ page: "prepare", jobId: "job-1" })).toBe(
      "#/jobs/job-1/prepare"
    )
    expect(routeToHash({ page: "handoff", jobId: "job-1" })).toBe(
      "#/jobs/job-1/handoff"
    )
  })
})
