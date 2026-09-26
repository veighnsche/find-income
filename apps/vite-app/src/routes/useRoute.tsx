import { useCallback, useEffect, useState } from "react"

export type Route =
  | { page: "today" }
  | { page: "search" }
  | { page: "jobs"; jobId: string | null }
  | { page: "check"; jobId: string }
  | { page: "answers"; jobId: string }
  | { page: "prepare"; jobId: string }
  | { page: "handoff"; jobId: string }
  | { page: "applications"; jobId: string | null }
  | { page: "not-found"; hash: string }

function decodeSegment(segment: string): string {
  try {
    return decodeURIComponent(segment)
  } catch {
    return segment
  }
}

export function parseHash(hash: string): Route {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash
  const path = raw.split("?")[0] ?? ""
  const segments = path
    .split("/")
    .filter((segment) => segment.length > 0)
    .map(decodeSegment)
  if (segments.length === 0) return { page: "today" }
  const [first, second, third, fourth, ...rest] = segments
  if (first === undefined || rest.length > 0)
    return { page: "not-found", hash: hash || "#/" }
  if (fourth !== undefined) {
    return { page: "not-found", hash: hash || "#/" }
  }
  if (third !== undefined) {
    if (first === "jobs" && second !== undefined && third === "check")
      return { page: "check", jobId: second }
    if (first === "jobs" && second !== undefined && third === "answers")
      return { page: "answers", jobId: second }
    if (first === "jobs" && second !== undefined && third === "prepare")
      return { page: "prepare", jobId: second }
    if (first === "jobs" && second !== undefined && third === "handoff")
      return { page: "handoff", jobId: second }
    return { page: "not-found", hash: hash || "#/" }
  }
  switch (first) {
    case "today":
      return second === undefined
        ? { page: "today" }
        : { page: "not-found", hash: hash }
    case "search":
      return second === undefined
        ? { page: "search" }
        : { page: "not-found", hash: hash }
    case "jobs":
      return second === undefined
        ? { page: "jobs", jobId: null }
        : { page: "jobs", jobId: second }
    case "applications":
      return second === undefined
        ? { page: "applications", jobId: null }
        : { page: "applications", jobId: second }
    default:
      return { page: "not-found", hash: hash }
  }
}

export function routeToHash(route: Route): string {
  switch (route.page) {
    case "today":
      return "#/today"
    case "search":
      return "#/search"
    case "jobs":
      return route.jobId === null
        ? "#/jobs"
        : `#/jobs/${encodeURIComponent(route.jobId)}`
    case "applications":
      return route.jobId === null
        ? "#/applications"
        : `#/applications/${encodeURIComponent(route.jobId)}`
    case "check":
      return `#/jobs/${encodeURIComponent(route.jobId)}/check`
    case "answers":
      return `#/jobs/${encodeURIComponent(route.jobId)}/answers`
    case "prepare":
      return `#/jobs/${encodeURIComponent(route.jobId)}/prepare`
    case "handoff":
      return `#/jobs/${encodeURIComponent(route.jobId)}/handoff`
    case "not-found":
      return route.hash.startsWith("#") ? route.hash : `#${route.hash}`
  }
}

export function useRoute(): [Route, (route: Route) => void] {
  const [route, setRoute] = useState<Route>(() =>
    parseHash(window.location.hash)
  )
  useEffect(() => {
    const onHashChange = () => setRoute(parseHash(window.location.hash))
    window.addEventListener("hashchange", onHashChange)
    return () => window.removeEventListener("hashchange", onHashChange)
  }, [])
  const navigate = useCallback((next: Route) => {
    const hash = routeToHash(next)
    if (window.location.hash === hash) {
      setRoute(parseHash(hash))
      return
    }
    window.location.hash = hash
  }, [])
  return [route, navigate]
}
