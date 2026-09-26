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
  draftConflict?: boolean
  editConflict?: boolean
}

function stubArtifactFetch(options: ArtifactStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  let live = options.set ?? heldSet()
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
      const path = new URL(url, "http://localhost").pathname
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
        path === "/api/v1/opportunities/job-1/artifacts/draft" &&
        method === "POST"
      ) {
        if (options.draftConflict === true)
          return jsonResponse(409, {
            error: { message: "Artifacts moved; reload first." },
          })
        live = readySet()
        return jsonResponse(201, live)
      }
      const editMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/artifacts\/([^/]+)$/
      )
      if (editMatch?.[1] !== undefined && method === "PUT") {
        if (options.editConflict === true)
          return jsonResponse(409, {
            error: { message: "Artifact version conflict." },
          })
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

function renderSection() {
  return render(
    <SessionProvider>
      <ArtifactsSection
        jobId="job-1"
        checkId="check-job-1"
        questionSetSha256="set-sha"
        workflowRevision={1}
      />
    </SessionProvider>
  )
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
    within(cv).getByText((_, element) =>
        element?.tagName === "PRE" && element.textContent === CV_TEXT)

    const subject = await screen.findByRole("region", {
      name: "Email subject",
    })
    within(subject).getByText("Held")
    within(subject).getByText(
      "Held: subject needs the hiring-team name."
    )
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

    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })

  it("drafts held types with the observed pins and shows the result", async () => {
    const { calls } = stubArtifactFetch()
    renderSection()

    await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      screen.getByRole("button", { name: "Draft held artifacts" })
    )
    // The post-mutation refresh remounts the section; re-query it.
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

  it("reports a draft conflict without inventing content", async () => {
    stubArtifactFetch({ draftConflict: true })
    renderSection()

    await screen.findByRole("region", { name: "Tailored CV" })
    fireEvent.click(
      screen.getByRole("button", { name: "Draft held artifacts" })
    )
    await screen.findByText(
      "These artifacts changed elsewhere. Reload this section, then try again."
    )
    expect(screen.queryByText(CV_TEXT)).toBeNull()
  })

  it("copies and downloads the displayed version bytes", async () => {
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
    const created: Array<{ blob: Blob; url: string }> = []
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL(blob: Blob): string {
          created.push({ blob, url: "blob:fake" })
          return "blob:fake"
        }
        static revokeObjectURL(): void {}
      }
    )
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
    await vi.waitFor(() => expect(created).toHaveLength(1))
    expect(await created[0]?.blob.text()).toBe(CV_TEXT)
  })

  it("says plainly when copy and download are unavailable", async () => {
    stubArtifactFetch({ set: readySet() })
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
    fireEvent.click(
      within(cv).getByRole("button", { name: "Save exact edit" })
    )
    // The post-save refresh remounts the section; re-query it.
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

  it("reports an edit conflict and keeps the typed text", async () => {
    stubArtifactFetch({ set: readySet(), editConflict: true })
    renderSection()

    const cv = await screen.findByRole("region", { name: "Tailored CV" })
    const box = within(cv).getByLabelText(
      "Exact edit (replaces v1 byte-exact, no model call)"
    ) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Owner rewrite." } })
    fireEvent.click(
      within(cv).getByRole("button", { name: "Save exact edit" })
    )
    await within(cv).findByText(
      "These artifacts changed elsewhere. Reload this section, then try again."
    )
    expect(
      (within(cv).getByLabelText(
        "Exact edit (replaces v1 byte-exact, no model call)"
      ) as HTMLTextAreaElement).value
    ).toBe("Owner rewrite.")
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
