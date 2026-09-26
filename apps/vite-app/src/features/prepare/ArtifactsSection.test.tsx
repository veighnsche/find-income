// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import type {
  ArtifactReadinessEntry,
  ArtifactReadinessSet,
  ArtifactView,
  CheckActivityPage,
  CheckStatusView,
  Clarification,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { ArtifactsSection } from "@/features/prepare/ArtifactsSection"
import { sessionFixture } from "@/pages/fixtures"

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

const CV_TEXT = "Jane Doe\nSenior Backend Engineer\n\nRemote-first."
const CV_V1_TEXT = "Jane Doe\nBackend Engineer (first draft)."
const CV_V2_TEXT = "Jane Doe\nSenior Backend Engineer (second draft)."
const REWRITTEN_CV = "Jane Doe\nSenior Backend Engineer (rewritten)."

function cvFixture(version = 1, content: string = CV_TEXT): ArtifactView {
  return {
    id: `artifact-cv-v${version}`,
    opportunityId: "job-1",
    type: "cv",
    version,
    content,
    basis: {
      factIds: ["fact-remote", "fact-senior"],
      answerRefs: [{ questionId: "q-remote", answerVersion: 1 }],
      checkSpans: [{ captureId: "cap-1", start: 30, end: 58 }],
    },
    createdAt: "2026-09-22T10:00:00Z",
    createdBy: { actorKind: "codex", actorId: "standard" },
  }
}

function heldSet(): ArtifactReadinessSet {
  const held = (
    type: ArtifactReadinessEntry["type"],
    required: boolean,
    reason: string
  ): ArtifactReadinessEntry => ({ type, required, state: "held", reason })
  return {
    opportunityId: "job-1",
    checkId: "check-job-1",
    checkStatus: "checked",
    entries: [
      held("cv", true, "Held: no draft yet; run Draft held artifacts."),
      held(
        "email_subject",
        true,
        "Held: email route needs a subject from verified facts."
      ),
      held(
        "email_body",
        true,
        "Held: email route needs a body from saved answers."
      ),
      {
        type: "cover_letter",
        required: false,
        state: "not_required",
        reason: "Email route needs no letter.",
      },
      {
        type: "form_values",
        required: false,
        state: "not_required",
        reason: "Email route has no portal form.",
      },
    ],
  }
}

function readySet(): ArtifactReadinessSet {
  return {
    opportunityId: "job-1",
    checkId: "check-job-1",
    checkStatus: "checked",
    entries: [
      {
        type: "cv",
        required: true,
        state: "ready",
        reason: "Drafted from 2 facts and 1 saved answer.",
        basis: "Tailored CV for the email route.",
        current: cvFixture(),
      },
      {
        type: "email_subject",
        required: true,
        state: "held",
        reason: "Held: subject needs the hiring-team name.",
      },
      {
        type: "email_body",
        required: true,
        state: "unresolved",
        reason: "Unresolved: the route evidence is ambiguous.",
      },
      {
        type: "cover_letter",
        required: false,
        state: "not_required",
        reason: "Email route needs no letter.",
      },
      {
        type: "form_values",
        required: false,
        state: "ready",
        reason: "Derived from 2 saved answers.",
        formValues: [
          {
            questionId: "q-remote",
            questionText: "Can you work remotely?",
            required: "required",
            kind: "free_text",
            state: "answered",
            text: "Yes — remote-first.",
          },
          {
            questionId: "q-start",
            questionText: "When can you start?",
            required: "optional",
            kind: "free_text",
            state: "blank",
            text: "",
          },
        ],
      },
    ],
  }
}

function readySetV2(): ArtifactReadinessSet {
  const set = readySet()
  return {
    ...set,
    entries: set.entries.map((entry) =>
      entry.type === "cv"
        ? { ...entry, current: cvFixture(2, CV_V2_TEXT) }
        : entry
    ),
  }
}

function clarificationFixture(
  status: Clarification["status"] = "open"
): Clarification {
  return {
    id: "cl-1",
    opportunityId: "job-1",
    checkId: "check-job-1",
    origin: "owner_clarification",
    requirement: {
      statement: "The vacancy asks for team-lead experience.",
      captureId: "cap-1",
      spanStart: 30,
      spanEnd: 58,
    },
    prompt: "How big was the team you led?",
    affectedWork: [{ kind: "artifact", id: "cv" }],
    status,
    ...(status === "answered"
      ? {
          answer: "Five engineers.",
          answeredAt: "2026-09-22T11:00:00Z",
          answeredBy: { actorKind: "owner", actorId: "owner" },
        }
      : {}),
    createdAt: "2026-09-22T10:30:00Z",
  }
}

const activityFixture: CheckActivityPage = {
  events: [
    {
      eventId: "evt-1",
      at: "2026-09-22T10:00:00Z",
      kind: "draft.completed",
      summary: "Standard drafted cv v1 from 2 facts and 1 saved answer.",
    },
    {
      eventId: "evt-2",
      at: "2026-09-22T10:01:00Z",
      kind: "draft.held",
      summary: "email_subject held: hiring-team name unknown.",
    },
  ],
}

interface ArtifactStubOptions {
  set?: ArtifactReadinessSet
  activity?: CheckActivityPage
  clarifications?: Clarification[]
  versions?: ArtifactView[]
  draftConflict?: boolean
  draftFailures?: number
  editConflict?: boolean
  editFailures?: number
  rewriteConflict?: boolean
  exportFailure?: boolean
  exportMediaType?: string
}

function stubArtifactFetch(options: ArtifactStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  let live = options.set ?? heldSet()
  let liveClarifications = options.clarifications ?? []
  let draftFailures = options.draftFailures ?? 0
  let editFailures = options.editFailures ?? 0
  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({
        url,
        method,
        body: typeof init?.body === "string" ? init.body : null,
      })
      const parsed = new URL(url, "http://localhost")
      const path = parsed.pathname
      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (
        path === "/api/v1/opportunities/job-1/artifacts" &&
        method === "GET"
      )
        return jsonResponse(200, live)
      if (
        path === "/api/v1/opportunities/job-1/artifacts/activity" &&
        method === "GET"
      )
        return jsonResponse(200, options.activity ?? { events: [] })
      if (
        path === "/api/v1/opportunities/job-1/clarifications" &&
        method === "GET"
      )
        return jsonResponse(200, { items: liveClarifications })
      const answerMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/clarifications\/([^/]+)\/answer$/
      )
      if (answerMatch?.[1] !== undefined && method === "POST") {
        const payload = JSON.parse(
          typeof init?.body === "string" ? init.body : "{}"
        ) as { text?: string }
        liveClarifications = liveClarifications.map((item) =>
          item.id === answerMatch[1]
            ? {
                ...item,
                status: "answered" as const,
                answer: payload.text ?? "",
                answeredAt: "2026-09-22T11:00:00Z",
                answeredBy: { actorKind: "owner", actorId: "owner" },
              }
            : item
        )
        return jsonResponse(
          200,
          liveClarifications.find((item) => item.id === answerMatch[1])
        )
      }
      if (
        path === "/api/v1/opportunities/job-1/artifacts/draft" &&
        method === "POST"
      ) {
        if (options.draftConflict === true)
          return jsonResponse(409, {
            error: { message: "Artifacts moved; reload first." },
          })
        if (draftFailures > 0) {
          draftFailures -= 1
          return jsonResponse(500, { error: { message: "Draft failed." } })
        }
        live = readySet()
        return jsonResponse(201, live)
      }
      const rewriteMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/artifacts\/([^/]+)\/rewrite$/
      )
      if (rewriteMatch?.[1] !== undefined && method === "POST") {
        if (options.rewriteConflict === true)
          return jsonResponse(409, {
            error: { message: "Artifact version conflict." },
          })
        const type = rewriteMatch[1]
        live = {
          ...live,
          entries: live.entries.map((entry) =>
            entry.type === type && entry.current !== undefined
              ? {
                  ...entry,
                  current: cvFixture(
                    entry.current.version + 1,
                    REWRITTEN_CV
                  ),
                }
              : entry
          ),
        }
        return jsonResponse(201, live)
      }
      const versionsMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/artifacts\/([^/]+)\/versions$/
      )
      if (versionsMatch?.[1] !== undefined && method === "GET") {
        return jsonResponse(200, {
          items: options.versions ?? [cvFixture(1, CV_V1_TEXT)],
        })
      }
      const exportMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/artifacts\/([^/]+)\/export$/
      )
      if (exportMatch?.[1] !== undefined && method === "GET") {
        if (options.exportFailure === true)
          return jsonResponse(500, { error: { message: "Export failed." } })
        const version = parsed.searchParams.get("version") ?? "live"
        return new Response(
          `exported:${exportMatch[1]}:v${version}\n${CV_TEXT}`,
          {
            status: 200,
            headers: {
              "Content-Type":
                options.exportMediaType ?? "text/markdown; charset=utf-8",
            },
          }
        )
      }
      const editMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/artifacts\/([^/]+)$/
      )
      if (editMatch?.[1] !== undefined && method === "PUT") {
        if (options.editConflict === true)
          return jsonResponse(409, {
            error: { message: "Artifact version conflict." },
          })
        if (editFailures > 0) {
          editFailures -= 1
          return jsonResponse(500, { error: { message: "Save failed." } })
        }
        const payload = JSON.parse(
          typeof init?.body === "string" ? init.body : "{}"
        ) as { expectedVersion?: number; content?: string }
        const saved = cvFixture(
          (payload.expectedVersion ?? 0) + 1,
          payload.content ?? ""
        )
        live = {
          ...live,
          entries: live.entries.map((entry) =>
            entry.type === "cv"
              ? { ...entry, state: "ready" as const, current: saved }
              : entry
          ),
        }
        return jsonResponse(200, saved)
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderSection(checkStatus: CheckStatusView["status"] = "checked") {
  return render(
    <SessionProvider>
      <ArtifactsSection
        jobId="job-1"
        checkId="check-job-1"
        checkStatus={checkStatus}
        questionSetSha256="set-sha"
        workflowRevision={1}
      />
    </SessionProvider>
  )
}

function stubDownloadCapture(): {
  blobs: Blob[]
  downloads: string[]
} {
  const blobs: Blob[] = []
  const downloads: string[] = []
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL(blob: Blob): string {
        blobs.push(blob)
        return "blob:fake"
      }
      static revokeObjectURL(): void {}
    }
  )
  const realCreate = document.createElement.bind(document)
  vi.spyOn(document, "createElement").mockImplementation(
    ((tagName: string, options?: ElementCreationOptions) => {
      const element = realCreate(tagName, options)
      if (tagName === "a") {
        const anchor = element as HTMLAnchorElement
        const originalClick = anchor.click.bind(anchor)
        anchor.click = () => {
          downloads.push(anchor.download)
          originalClick()
        }
      }
      return element
    }) as typeof document.createElement
  )
  return { blobs, downloads }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("ArtifactsSection", () => {
  it("mounts GET-only and labels every route state honestly", async () => {
    const { calls } = stubArtifactFetch({ set: readySet() })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    within(cv).getByText("Ready")
    within(cv).getByText("Drafted from 2 facts and 1 saved answer.")
    within(cv).getByText(
      (_, element) =>
        element?.tagName === "PRE" && element.textContent === CV_TEXT
    )

    const subject = await screen.findByRole("region", {
      name: "Email subject",
    })
    within(subject).getByText("Held")
    within(subject).getByText("Held: subject needs the hiring-team name.")
    expect(
      within(subject).queryByRole("button", { name: /Copy Email subject/ })
    ).toBeNull()

    const body = await screen.findByRole("region", {
      name: "Motivation email",
    })
    within(body).getByText("Unresolved")

    const letter = await screen.findByRole("region", {
      name: "Motivation letter",
    })
    within(letter).getByText("Not required")

    await screen.findByRole("region", { name: "Owner questions" })
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })

  it("drafts held types with the observed pins and shows the result", async () => {
    const { calls } = stubArtifactFetch()
    renderSection()

    await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(screen.getByRole("button", { name: "Draft held artifacts" }))
    await screen.findByText("Loading route artifacts…")
    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    await within(cv).findByText(
      (_, element) =>
        element?.tagName === "PRE" && element.textContent === CV_TEXT
    )

    const posts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/artifacts/draft"
    )
    expect(posts).toHaveLength(1)
    const payload = JSON.parse(posts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["expectedCheckId"]).toBe("check-job-1")
    expect(payload["expectedQuestionSetSha256"]).toBe("set-sha")
    expect(payload["expectedWorkflowRevision"]).toBe(1)
    expect(typeof payload["requestKey"]).toBe("string")
  })

  it("retries a failed draft with the same idempotent request key", async () => {
    const { calls } = stubArtifactFetch({ draftFailures: 1 })
    renderSection()

    await screen.findByRole("region", { name: "Tailored CV" })
    const draft = screen.getByRole("button", { name: "Draft held artifacts" })
    fireEvent.click(draft)
    await screen.findByText("Draft failed.")
    fireEvent.click(screen.getByRole("button", { name: "Draft held artifacts" }))
    await screen.findByText("Loading route artifacts…")
    await screen.findByRole("region", { name: "Tailored CV" })

    const posts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/artifacts/draft"
    )
    expect(posts).toHaveLength(2)
    const first = JSON.parse(posts[0]?.body ?? "{}") as Record<string, unknown>
    const second = JSON.parse(posts[1]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(second["requestKey"]).toBe(first["requestKey"])
  })

  it("reports a draft conflict without inventing content", async () => {
    stubArtifactFetch({ draftConflict: true })
    renderSection()

    await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(screen.getByRole("button", { name: "Draft held artifacts" }))
    await screen.findByText(
      "These artifacts changed elsewhere. Reload this section, then try again."
    )
    expect(screen.queryByText(CV_TEXT)).toBeNull()
  })

  it("copies current bytes and downloads the canonical export file", async () => {
    stubArtifactFetch({ set: readySet() })
    const written: string[] = []
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: vi.fn(async (text: string) => {
          written.push(text)
        }),
      },
    })
    const { blobs, downloads } = stubDownloadCapture()
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Copy Tailored CV v1" })
    )
    await within(cv).findByText("Copied.")
    expect(written).toEqual([CV_TEXT])

    fireEvent.click(
      within(cv).getByRole("button", { name: "Download Tailored CV v1" })
    )
    await vi.waitFor(() => expect(blobs).toHaveLength(1))
    expect(await blobs[0]?.text()).toBe(`exported:cv:v1\n${CV_TEXT}`)
    expect(downloads).toEqual(["job-1-cv-v1.md"])
  })

  it("says plainly when copy and export are unavailable", async () => {
    stubArtifactFetch({ set: readySet(), exportFailure: true })
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined,
    })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Copy Tailored CV v1" })
    )
    await within(cv).findByText("Copy failed — select the text manually.")

    fireEvent.click(
      within(cv).getByRole("button", { name: "Download Tailored CV v1" })
    )
    await within(cv).findByText(
      "Export failed — try again or copy the text instead."
    )
  })

  it("saves an exact per-artifact edit fenced on the displayed version", async () => {
    const { calls } = stubArtifactFetch({ set: readySet() })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    const box = within(cv).getByLabelText(
      "Exact edit (replaces v1 byte-exact, no model call)"
    ) as HTMLTextAreaElement
    expect(box.value).toBe(CV_TEXT)
    const edited = `${CV_TEXT}\n\nAvailable from October.`
    fireEvent.change(box, { target: { value: edited } })
    fireEvent.click(within(cv).getByRole("button", { name: "Save exact edit" }))
    await screen.findByText("Loading route artifacts…")
    const refreshed = await screen.findByRole("region", {
      name: "Tailored CV",
    })
    await within(refreshed).findByText(
      (_, element) =>
        element?.tagName === "PRE" &&
        (element.textContent ?? "").includes("Available from October.")
    )

    const puts = calls.filter((call) => call.method === "PUT")
    expect(puts).toHaveLength(1)
    expect(puts[0]?.url).toBe("/api/v1/opportunities/job-1/artifacts/cv")
    const payload = JSON.parse(puts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["expectedVersion"]).toBe(1)
    expect(payload["content"]).toBe(edited)
    expect(typeof payload["requestKey"]).toBe("string")
  })

  it("retries a failed edit with the same idempotent request key", async () => {
    const { calls } = stubArtifactFetch({ set: readySet(), editFailures: 1 })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    const box = within(cv).getByLabelText(
      "Exact edit (replaces v1 byte-exact, no model call)"
    ) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Owner rewrite." } })
    fireEvent.click(within(cv).getByRole("button", { name: "Save exact edit" }))
    await within(cv).findByText("Save failed.")
    fireEvent.click(within(cv).getByRole("button", { name: "Save exact edit" }))
    await screen.findByText("Loading route artifacts…")
    await screen.findByRole("region", { name: "Tailored CV" })

    const puts = calls.filter((call) => call.method === "PUT")
    expect(puts).toHaveLength(2)
    const first = JSON.parse(puts[0]?.body ?? "{}") as Record<string, unknown>
    const second = JSON.parse(puts[1]?.body ?? "{}") as Record<string, unknown>
    expect(second["requestKey"]).toBe(first["requestKey"])
    expect(second["content"]).toBe("Owner rewrite.")
  })

  it("reports an edit conflict and keeps the typed text", async () => {
    stubArtifactFetch({ set: readySet(), editConflict: true })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    const box = within(cv).getByLabelText(
      "Exact edit (replaces v1 byte-exact, no model call)"
    ) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Owner rewrite." } })
    fireEvent.click(within(cv).getByRole("button", { name: "Save exact edit" }))
    await within(cv).findByText(
      "These artifacts changed elsewhere. Reload this section, then try again."
    )
    expect(
      (
        within(cv).getByLabelText(
          "Exact edit (replaces v1 byte-exact, no model call)"
        ) as HTMLTextAreaElement
      ).value
    ).toBe("Owner rewrite.")
  })

  it("rewrites one artifact with the instruction verbatim and shows v2", async () => {
    const { calls } = stubArtifactFetch({ set: readySet() })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    const box = within(cv).getByLabelText(
      "Request rewrite (Standard drafts v2 from the same facts and answers)"
    ) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Shorter." } })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Request rewrite" })
    )
    await screen.findByText("Loading route artifacts…")
    const refreshed = await screen.findByRole("region", {
      name: "Tailored CV",
    })
    await within(refreshed).findByText(
      (_, element) =>
        element?.tagName === "PRE" && element.textContent === REWRITTEN_CV
    )

    const posts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/artifacts/cv/rewrite"
    )
    expect(posts).toHaveLength(1)
    const payload = JSON.parse(posts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["expectedVersion"]).toBe(1)
    expect(payload["instruction"]).toBe("Shorter.")
    expect(typeof payload["requestKey"]).toBe("string")
  })

  it("reports a rewrite conflict and keeps the instruction", async () => {
    stubArtifactFetch({ set: readySet(), rewriteConflict: true })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    const box = within(cv).getByLabelText(
      "Request rewrite (Standard drafts v2 from the same facts and answers)"
    ) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Shorter." } })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Request rewrite" })
    )
    await within(cv).findByText(
      "These artifacts changed elsewhere. Reload this section, then try again."
    )
    expect(
      (
        within(cv).getByLabelText(
          "Request rewrite (Standard drafts v2 from the same facts and answers)"
        ) as HTMLTextAreaElement
      ).value
    ).toBe("Shorter.")
    expect(screen.queryByText(REWRITTEN_CV)).toBeNull()
  })

  it("opens version history GET-only and copies the shown previous version", async () => {
    const { calls } = stubArtifactFetch({
      set: readySetV2(),
      versions: [cvFixture(1, CV_V1_TEXT), cvFixture(2, CV_V2_TEXT)],
    })
    const written: string[] = []
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: vi.fn(async (text: string) => {
          written.push(text)
        }),
      },
    })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Show version history" })
    )
    await within(cv).findByText("Version history (2 stored, oldest first)")
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }

    fireEvent.click(
      within(cv).getByRole("button", { name: "Show Tailored CV v1" })
    )
    await within(cv).findByText(/Showing v1 \(previous version — read-only\)/)
    fireEvent.click(
      within(cv).getByRole("button", { name: "Copy shown Tailored CV v1" })
    )
    await within(cv).findByText("Copied.")
    expect(written).toEqual([CV_V1_TEXT])
  })

  it("downloads the shown previous version from the canonical export", async () => {
    stubArtifactFetch({
      set: readySetV2(),
      versions: [cvFixture(1, CV_V1_TEXT), cvFixture(2, CV_V2_TEXT)],
    })
    const { blobs, downloads } = stubDownloadCapture()
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Show version history" })
    )
    await within(cv).findByText("Version history (2 stored, oldest first)")
    fireEvent.click(
      within(cv).getByRole("button", { name: "Show Tailored CV v1" })
    )
    await within(cv).findByText(/Showing v1 \(previous version — read-only\)/)
    fireEvent.click(
      within(cv).getByRole("button", { name: "Download shown Tailored CV v1" })
    )
    await vi.waitFor(() => expect(blobs).toHaveLength(1))
    expect(await blobs[0]?.text()).toBe(`exported:cv:v1\n${CV_TEXT}`)
    expect(downloads).toEqual(["job-1-cv-v1.md"])
  })

  it("saves an owner answer exactly and resumes only via an explicit draft", async () => {
    const { calls } = stubArtifactFetch({
      clarifications: [clarificationFixture("open")],
    })
    renderSection()

    const questions = await screen.findByRole("region", {
      name: "Owner questions",
    })
    within(questions).getByText("1 open")
    within(questions).getByText("How big was the team you led?")
    within(questions).getByText(
      "Needed for: The vacancy asks for team-lead experience."
    )
    within(questions).getByText("Holds: artifact: cv")

    const box = within(questions).getByLabelText(
      "Your answer (saved exactly, no model call)"
    ) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Five engineers." } })
    fireEvent.click(
      within(questions).getByRole("button", { name: "Save answer" })
    )
    // The accepted answer refreshes the section; re-query after remount.
    await screen.findByText("Your saved answer")
    await screen.findByText("Five engineers.")
    for (const call of calls.filter(
      (call) => !call.url.includes("/auth/session")
    )) {
      expect(call.url).toContain("/opportunities/job-1/")
    }

    const answers = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url ===
          "/api/v1/opportunities/job-1/clarifications/cl-1/answer"
    )
    expect(answers).toHaveLength(1)
    expect(JSON.parse(answers[0]?.body ?? "{}")).toMatchObject({
      text: "Five engineers.",
    })

    const refreshed = await screen.findByRole("region", {
      name: "Owner questions",
    })
    const resume = within(refreshed).getByRole("button", {
      name: "Resume preparation",
    })
    fireEvent.click(resume)
    await screen.findByText("Loading route artifacts…")
    const drafts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/artifacts/draft"
    )
    expect(drafts).toHaveLength(1)
  })

  it("keeps prior content readable but outdated under a stale check", async () => {
    const { calls } = stubArtifactFetch({ set: readySet() })
    renderSection("outdated")

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    // Wait for every section read so no loading block shares the banner role.
    await screen.findByRole("region", { name: "Owner questions" })
    const banner = screen.getByRole("status")
    expect(banner.textContent).toContain("role changed after these materials")
    within(cv).getByText("Outdated")
    expect(within(cv).queryByText("Ready")).toBeNull()
    within(cv).getByText(
      (_, element) =>
        element?.tagName === "PRE" && element.textContent === CV_TEXT
    )
    // Prior form values stay per-field readable too.
    const form = await screen.findByRole("region", { name: "Form values" })
    within(form).getByText("Outdated")
    within(form).getByText("Yes — remote-first.")

    const draft = screen.getByRole("button", { name: "Draft held artifacts" })
    expect(draft.hasAttribute("disabled")).toBe(true)
    const save = within(cv).getByRole("button", { name: "Save exact edit" })
    expect(save.hasAttribute("disabled")).toBe(true)
    const rewrite = within(cv).getByRole("button", { name: "Request rewrite" })
    expect(rewrite.hasAttribute("disabled")).toBe(true)

    // History stays inspectable: reads make no model call or write.
    fireEvent.click(
      within(cv).getByRole("button", { name: "Show version history" })
    )
    await within(cv).findByText(/Version history \(/)
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })

  it("lists derived form values with per-value copy and an answers link", async () => {
    stubArtifactFetch({ set: readySet() })
    const written: string[] = []
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: vi.fn(async (text: string) => {
          written.push(text)
        }),
      },
    })
    renderSection()

    const form = await screen.findByRole("region", { name: "Form values" })
    within(form).getByText("Can you work remotely?")
    within(form).getByText("Yes — remote-first.")
    within(form).getByText("Blank — nothing copyable.")
    const answers = within(form).getByRole("link", {
      name: "Open answers",
    }) as HTMLAnchorElement
    expect(answers.getAttribute("href")).toBe("#/jobs/job-1/answers")
    expect(
      within(form).queryByRole("button", { name: "Save exact edit" })
    ).toBeNull()

    fireEvent.click(
      within(form).getByRole("button", {
        name: "Copy value for Can you work remotely?",
      })
    )
    await within(form).findByText("Copied.")
    expect(written).toEqual(["Yes — remote-first."])
  })

  it("downloads all form values from the live server derivation", async () => {
    stubArtifactFetch({ set: readySet() })
    const { blobs, downloads } = stubDownloadCapture()
    renderSection()

    const form = await screen.findByRole("region", { name: "Form values" })
    fireEvent.click(
      within(form).getByRole("button", { name: "Download all values" })
    )
    await vi.waitFor(() => expect(blobs).toHaveLength(1))
    expect(await blobs[0]?.text()).toBe(`exported:form_values:vlive\n${CV_TEXT}`)
    expect(downloads).toEqual(["job-1-form_values-live.md"])
  })

  it("shows the prepare activity journal in a disclosure", async () => {
    stubArtifactFetch({ set: readySet(), activity: activityFixture })
    renderSection()

    await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      screen.getByRole("button", { name: /Prepare materials · activity/ })
    )
    await screen.findByText(
      "Standard drafted cv v1 from 2 facts and 1 saved answer."
    )
    screen.getByText("email_subject held: hiring-team name unknown.")
  })
})
