// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type {
  ApplicationPackDetail,
  CheckStatusView,
  CheckView,
  MaterialStatusView,
  MaterialVersion,
  OpportunityView,
  RoleWorkflowState,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import {
  PreparePage,
  buildMaterialEditRequest,
  buildMaterialPrepareRequest,
  buildMaterialRewriteRequest,
  countBytes,
  countRunes,
  renderPackText,
} from "@/features/prepare"
import { roleWorkflowFixture, sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function opportunityFixture(id: string): OpportunityView {
  return {
    opportunity: {
      id,
      companyId: "company-1",
      title: "Backend Engineer",
      kind: "employment",
      sourceUrl: `https://example.com/jobs/${id}`,
      originalText: `Original posting text for ${id}.`,
      notes: "",
      stage: "new",
      workPattern: "remote",
      locationText: "Berlin",
      postedOn: "2026-09-01",
      deadlineOn: "",
      revision: 2,
      createdAt: "2026-09-10T09:00:00Z",
      updatedAt: "2026-09-12T09:00:00Z",
      compensation: {},
    },
    likelyDuplicates: [],
  }
}

function checkFixture(
  opportunityId: string,
  overall: CheckStatusView["status"] = "checked"
): CheckStatusView {
  const check: CheckView = {
    id: `check-${opportunityId}`,
    opportunityId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status:
      overall === "blocked"
        ? "blocked"
        : overall === "checking"
          ? "checking"
          : "checked",
    vacancy: {
      captureIds: ["cap-1"],
      evidenceSourceIds: ["src-1"],
      completeness: "complete",
      sourceUrl: `https://example.com/jobs/${opportunityId}`,
      retrievedAt: "2026-09-20T10:00:00Z",
    },
    requestedDocuments: [],
    route: {
      judgment: "application_route",
      sourceExcerpt: "Apply through the portal.",
      observedAt: "2026-09-20T10:00:00Z",
    },
    gaps: [],
    questions: [
      {
        id: "q-remote",
        checkId: `check-${opportunityId}`,
        ordinal: 0,
        text: "Can you work remotely?",
        required: "required",
        kind: "free_text",
        sourceSpan: { captureId: "cap-1", start: 30, end: 58 },
        sourceExcerpt: "Can you work remotely?",
        textSha256: "sha-remote",
      },
      {
        id: "q-start",
        checkId: `check-${opportunityId}`,
        ordinal: 1,
        text: "When can you start?",
        required: "optional",
        kind: "free_text",
        sourceSpan: { captureId: "cap-1", start: 60, end: 80 },
        sourceExcerpt: "When can you start?",
        textSha256: "sha-start",
      },
    ],
    questionSetSha256: "set-sha",
    questionSetVersion: 1,
    createdAt: "2026-09-20T10:00:00Z",
    createdBy: { actorKind: "codex", actorId: "codex" },
  }
  if (overall === "not_checked") return { status: overall }
  return { status: overall, check }
}

function materialVersionFixture(
  overrides: Partial<MaterialVersion> = {}
): MaterialVersion {
  return {
    packId: "pack-1",
    version: 1,
    opportunityId: "job-1",
    opportunityRevision: 2,
    profileRevision: 3,
    checkId: "check-job-1",
    questionSetSha256: "set-sha",
    answers: [
      {
        questionId: "q-remote",
        questionTextSha256: "sha-remote",
        answerVersion: 2,
        textSha256: "text-sha-remote",
      },
      {
        questionId: "q-start",
        questionTextSha256: "sha-start",
        answerVersion: 0,
        textSha256: "text-sha-start",
      },
    ],
    readiness: { ready: true, missingRequired: [], held: [] },
    provenance: {
      origin: "prepared",
      sourceShas: ["career-sha-1", "role-sha-2"],
    },
    createdAt: "2026-09-22T10:00:00Z",
    createdBy: { actorKind: "codex", actorId: "codex" },
    ...overrides,
  }
}

function packDetailFixture(): ApplicationPackDetail {
  return {
    id: "pack-1",
    opportunityId: "job-1",
    opportunityRevision: 2,
    profileRevision: 3,
    version: 1,
    contentSha256: "pack-content-sha-256",
    createdAt: "2026-09-22T10:00:00Z",
    manifest: {
      role: {
        opportunityId: "job-1",
        opportunityRevision: 2,
        profileRevision: 3,
        title: "Backend Engineer",
        company: "Example Corp",
        sourceUrl: "https://example.com/jobs/job-1",
        description: "Build things.",
        destination: "portal",
      },
      sources: [],
      draft: {
        focus: { text: "Focus line.", citations: [] },
        cover: [{ text: "Cover line.", citations: [] }],
        answers: [
          {
            question: "Can you work remotely?",
            lines: [{ text: "I work remotely.", citations: [] }],
          },
        ],
        materialUnknowns: [],
        relevance: [],
      },
      templateSha256: "template-sha",
    },
  }
}

interface FetchCall {
  url: string
  method: string
  body: string | null
}

interface PrepareStubOptions {
  opportunities?: Record<string, OpportunityView | null>
  workflows?: Record<string, RoleWorkflowState | null>
  checks?: Record<string, CheckStatusView | null>
  materials?: Record<string, MaterialStatusView | null>
  packs?: Record<string, ApplicationPackDetail | null>
  postPrepare?: (jobId: string, body: unknown) => Response
  putMaterials?: (jobId: string, body: unknown) => Response
  postRewrite?: (jobId: string, body: unknown) => Response
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function notFound(message: string): Response {
  return jsonResponse(404, { error: { message } })
}

function stubPrepareFetch(options: PrepareStubOptions = {}): {
  calls: FetchCall[]
} {
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
      const body = typeof init?.body === "string" ? init.body : null
      calls.push({ url, method, body })
      const parsed = new URL(url, "http://localhost")
      const path = parsed.pathname

      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)

      const packMatch = path.match(/^\/api\/v1\/application-packs\/([^/]+)$/)
      if (packMatch?.[1] !== undefined) {
        const entry =
          options.packs?.[decodeURIComponent(packMatch[1])] ?? null
        return entry === null
          ? notFound("Pack not found.")
          : jsonResponse(200, entry)
      }

      const match = path.match(/^\/api\/v1\/opportunities\/([^/]+)(\/.*)?$/)
      if (match?.[1] !== undefined) {
        const id = decodeURIComponent(match[1])
        const suffix = match[2] ?? ""
        const view = options.opportunities?.[id] ?? null
        if (suffix === "")
          return view === null
            ? notFound("Opportunity not found.")
            : jsonResponse(200, view)
        if (view === null) return notFound("Opportunity not found.")
        if (suffix === "/workflow") {
          const entry = options.workflows?.[id] ?? null
          return entry === null
            ? notFound("Role is not selected.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/checks/current") {
          const entry = options.checks?.[id] ?? null
          return entry === null
            ? notFound("Check not found.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/materials/current" && method === "GET") {
          const entry = options.materials?.[id] ?? null
          return entry === null
            ? notFound("Materials not found.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/materials/current" && method === "PUT") {
          if (options.putMaterials !== undefined)
            return options.putMaterials(
              id,
              body === null ? null : JSON.parse(body)
            )
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
        if (suffix === "/materials/prepare" && method === "POST") {
          if (options.postPrepare !== undefined)
            return options.postPrepare(
              id,
              body === null ? null : JSON.parse(body)
            )
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
        if (suffix === "/materials/rewrite" && method === "POST") {
          if (options.postRewrite !== undefined)
            return options.postRewrite(
              id,
              body === null ? null : JSON.parse(body)
            )
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderPreparePage(jobId: string) {
  return render(
    <SessionProvider>
      <PreparePage jobId={jobId} />
    </SessionProvider>
  )
}

function preparedOptions(
  overrides: Partial<PrepareStubOptions> = {}
): PrepareStubOptions {
  return {
    opportunities: { "job-1": opportunityFixture("job-1") },
    workflows: {
      "job-1": roleWorkflowFixture("job-1", "answered", { revision: 1 }),
    },
    checks: { "job-1": checkFixture("job-1") },
    materials: {
      "job-1": { status: "prepared", current: materialVersionFixture() },
    },
    packs: { "pack-1": packDetailFixture() },
    ...overrides,
  }
}

describe("prepare payloads", () => {
  it("builds the exact prepare payload from observed pins", () => {
    expect(
      buildMaterialPrepareRequest("key-1", "check-1", "set-sha", 4)
    ).toEqual({
      requestKey: "key-1",
      expectedCheckId: "check-1",
      expectedQuestionSetSha256: "set-sha",
      expectedWorkflowRevision: 4,
    })
  })

  it("builds the exact edit payload without touching the text", () => {
    expect(buildMaterialEditRequest("key-2", 3, "  spaced ✅ \n")).toEqual({
      requestKey: "key-2",
      expectedVersion: 3,
      text: "  spaced ✅ \n",
    })
  })

  it("omits a blank rewrite instruction and keeps a set one verbatim", () => {
    expect(buildMaterialRewriteRequest("key-3", 1, "")).toEqual({
      requestKey: "key-3",
      expectedVersion: 1,
    })
    expect(
      buildMaterialRewriteRequest("key-3", 1, "  Shorter ✅ \n")
    ).toEqual({
      requestKey: "key-3",
      expectedVersion: 1,
      instruction: "  Shorter ✅ \n",
    })
  })

  it("counts runes and bytes for multibyte text", () => {
    expect(countRunes("✅")).toBe(1)
    expect(countBytes("✅")).toBe(3)
  })

  it("renders pack text deterministically and skips empty parts", () => {
    const pack = packDetailFixture()
    expect(renderPackText(pack)).toBe(
      "Focus line.\n\nCover line.\n\nCan you work remotely?\n\nI work remotely."
    )
    const sparse: ApplicationPackDetail = {
      ...pack,
      manifest: {
        ...pack.manifest,
        draft: { ...pack.manifest.draft, focus: { text: "", citations: [] } },
      },
    }
    expect(renderPackText(sparse)).toBe(
      "Cover line.\n\nCan you work remotely?\n\nI work remotely."
    )
  })
})

describe("prepare page reads", () => {
  it("mounts GET-only: no prepare, edit, or rewrite commission", async () => {
    const { calls } = stubPrepareFetch(preparedOptions())
    renderPreparePage("job-1")

    expect(await screen.findByText("Version 1")).toBeDefined()
    expect(
      screen.getByRole("heading", { name: "Putting your application together" })
    ).toBeDefined()
    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })

  it("shows the not-selected state without material reads", async () => {
    const { calls } = stubPrepareFetch(
      preparedOptions({ workflows: { "job-1": null } })
    )
    renderPreparePage("job-1")

    expect(await screen.findByText("Role not selected")).toBeDefined()
    expect(
      calls.some((call) => call.url.includes("/materials/"))
    ).toBe(false)
    expect(
      calls.some((call) => call.url.includes("/checks/"))
    ).toBe(false)
  })

  it("blocks preparation until a check completes", async () => {
    const { calls } = stubPrepareFetch(
      preparedOptions({
        checks: { "job-1": checkFixture("job-1", "not_checked") },
        materials: { "job-1": { status: "not_prepared" } },
      })
    )
    renderPreparePage("job-1")

    expect(await screen.findByText("No check yet")).toBeDefined()
    expect(
      screen.queryByRole("button", { name: "Prepare application" })
    ).toBeNull()
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })

  it("shows version, provenance, refs, and materials for a ready version", async () => {
    stubPrepareFetch(preparedOptions())
    renderPreparePage("job-1")

    expect(await screen.findByText("Version 1")).toBeDefined()
    expect(
      await screen.findAllByText("Prepared from verified facts")
    ).toHaveLength(2)
    expect(await screen.findByText("career-sha-1")).toBeDefined()
    expect(
      await screen.findByText("1. Can you work remotely?")
    ).toBeDefined()
    expect(await screen.findByText(/Saved answer v2/)).toBeDefined()
    expect(
      await screen.findByText(/Drafted into this version/)
    ).toBeDefined()
    expect(await screen.findByText("Focus line.")).toBeDefined()
    expect(await screen.findByText("I work remotely.")).toBeDefined()
    const review = (await screen.findByText(
      "Continue to review v1"
    )) as HTMLAnchorElement
    expect(review.getAttribute("href")).toBe("#/applications/job-1/review")
  })

  it("keeps held versions visibly held with no review link", async () => {
    stubPrepareFetch(
      preparedOptions({
        materials: {
          "job-1": {
            status: "held",
            current: materialVersionFixture({
              readiness: {
                ready: false,
                missingRequired: ["q-remote"],
                held: ["q-remote"],
              },
            }),
          },
        },
      })
    )
    renderPreparePage("job-1")

    expect(await screen.findByText("Held")).toBeDefined()
    expect(
      await screen.findByText(/still needs an owner-known fact/)
    ).toBeDefined()
    expect(
      await screen.findByText("Missing required: Can you work remotely?")
    ).toBeDefined()
    expect(
      await screen.findByText(
        "This version is held: every required item needs an answer before review."
      )
    ).toBeDefined()
    expect(screen.queryByText(/Continue to review/)).toBeNull()
  })

  it("offers re-prepare for outdated versions and marks the stale base", async () => {
    stubPrepareFetch(
      preparedOptions({
        materials: {
          "job-1": {
            status: "outdated",
            current: materialVersionFixture(),
          },
        },
      })
    )
    renderPreparePage("job-1")

    expect(
      await screen.findByRole("button", { name: "Re-prepare application" })
    ).toBeDefined()
    expect(await screen.findByText("Version 1")).toBeDefined()
    expect(await screen.findByText("Outdated")).toBeDefined()
    expect(screen.queryByText(/Continue to review/)).toBeNull()
  })
})

describe("prepare action", () => {
  it("prepares explicitly with observed pins, then shows the new version", async () => {
    const options = preparedOptions({
      materials: { "job-1": { status: "not_prepared" } },
    })
    const prepared: MaterialStatusView = {
      status: "prepared",
      current: materialVersionFixture(),
    }
    const seen: unknown[] = []
    options.postPrepare = (_jobId, body) => {
      seen.push(body)
      if (options.materials !== undefined)
        options.materials["job-1"] = prepared
      return jsonResponse(201, prepared)
    }
    const { calls } = stubPrepareFetch(options)
    renderPreparePage("job-1")

    const button = await screen.findByRole("button", {
      name: "Prepare application",
    })
    expect(
      calls.some(
        (call) =>
          call.url.includes("/materials/prepare") && call.method === "POST"
      )
    ).toBe(false)
    fireEvent.click(button)

    expect(await screen.findByText("Version 1")).toBeDefined()
    expect(seen).toHaveLength(1)
    const payload = seen[0] as Record<string, unknown>
    expect(payload["expectedCheckId"]).toBe("check-job-1")
    expect(payload["expectedQuestionSetSha256"]).toBe("set-sha")
    expect(payload["expectedWorkflowRevision"]).toBe(1)
    expect(typeof payload["requestKey"]).toBe("string")
    expect((payload["requestKey"] as string).length).toBeGreaterThan(0)
  })

  it("reports a prepare conflict honestly", async () => {
    const options = preparedOptions({
      materials: { "job-1": { status: "not_prepared" } },
    })
    options.postPrepare = () =>
      jsonResponse(409, { error: { message: "Version conflict." } })
    stubPrepareFetch(options)
    renderPreparePage("job-1")

    fireEvent.click(
      await screen.findByRole("button", { name: "Prepare application" })
    )
    expect(
      await screen.findByText(/These materials changed elsewhere/)
    ).toBeDefined()
  })
})

describe("direct edit", () => {
  it("saves exact bytes with the observed version", async () => {
    const options = preparedOptions()
    const edited = materialVersionFixture({
      version: 2,
      packId: "pack-1",
      provenance: { origin: "direct_edit", sourceShas: ["edit-sha"] },
    })
    const seen: unknown[] = []
    options.putMaterials = (_jobId, body) => {
      seen.push(body)
      if (options.materials !== undefined)
        options.materials["job-1"] = { status: "prepared", current: edited }
      return jsonResponse(201, edited)
    }
    stubPrepareFetch(options)
    renderPreparePage("job-1")

    const box = (await screen.findByLabelText(
      /Material text \(replaces v1 byte-exact\)/
    )) as HTMLTextAreaElement
    expect(box.value).toBe("")
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Start from current rendering",
      })
    )
    expect(box.value).toBe(
      "Focus line.\n\nCover line.\n\nCan you work remotely?\n\nI work remotely."
    )
    const editedText = `${box.value}\n\nOwner line ✅.`
    fireEvent.change(box, { target: { value: editedText } })
    fireEvent.click(
      await screen.findByRole("button", { name: "Save exact edit" })
    )

    await waitFor(() => expect(seen).toHaveLength(1))
    const payload = seen[0] as Record<string, unknown>
    expect(payload["expectedVersion"]).toBe(1)
    expect(payload["text"]).toBe(editedText)
    expect(await screen.findByText("Version 2")).toBeDefined()
    expect(
      await screen.findAllByText("Direct owner edit (no model)")
    ).toHaveLength(2)
  })

  it("keeps the box text and reports a 409 conflict honestly", async () => {
    const options = preparedOptions()
    options.putMaterials = () =>
      jsonResponse(409, { error: { message: "Version conflict." } })
    stubPrepareFetch(options)
    renderPreparePage("job-1")

    const box = (await screen.findByLabelText(
      /Material text \(replaces v1 byte-exact\)/
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Owner replacement." } })
    fireEvent.click(
      await screen.findByRole("button", { name: "Save exact edit" })
    )

    expect(
      await screen.findByText(/These materials changed elsewhere/)
    ).toBeDefined()
    expect(
      (screen.getByLabelText(
        /Material text \(replaces v1 byte-exact\)/
      ) as HTMLTextAreaElement).value
    ).toBe("Owner replacement.")
  })
})

describe("rewrite", () => {
  it("rewrites only on explicit click with the verbatim instruction", async () => {
    const options = preparedOptions()
    const rewritten = materialVersionFixture({
      version: 2,
      provenance: { origin: "rewrite", sourceShas: ["rw-sha"], rewriteOf: 1 },
    })
    const seen: unknown[] = []
    options.postRewrite = (_jobId, body) => {
      seen.push(body)
      if (options.materials !== undefined)
        options.materials["job-1"] = { status: "prepared", current: rewritten }
      return jsonResponse(201, rewritten)
    }
    const { calls } = stubPrepareFetch(options)
    renderPreparePage("job-1")

    const box = (await screen.findByLabelText(
      /Instruction \(optional/
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "  Shorter ✅ \n" } })
    expect(
      calls.some(
        (call) =>
          call.url.includes("/materials/rewrite") && call.method === "POST"
      )
    ).toBe(false)
    fireEvent.click(await screen.findByRole("button", { name: "Request rewrite" }))

    await waitFor(() => expect(seen).toHaveLength(1))
    const payload = seen[0] as Record<string, unknown>
    expect(payload["expectedVersion"]).toBe(1)
    expect(payload["instruction"]).toBe("  Shorter ✅ \n")
    expect(await screen.findByText("Version 2")).toBeDefined()
    expect(await screen.findByText("Explicit rewrite")).toBeDefined()
    expect(await screen.findByText(/of v1/)).toBeDefined()
  })

  it("omits a blank instruction and caps the instruction at 2000 characters", async () => {
    const options = preparedOptions()
    const rewritten = materialVersionFixture({ version: 2 })
    const seen: unknown[] = []
    options.postRewrite = (_jobId, body) => {
      seen.push(body)
      if (options.materials !== undefined)
        options.materials["job-1"] = { status: "prepared", current: rewritten }
      return jsonResponse(201, rewritten)
    }
    stubPrepareFetch(options)
    renderPreparePage("job-1")

    const button = await screen.findByRole("button", {
      name: "Request rewrite",
    })
    fireEvent.click(button)
    await waitFor(() => expect(seen).toHaveLength(1))
    expect(seen[0]).not.toHaveProperty("instruction")

    const box = (await screen.findByLabelText(
      /Instruction \(optional/
    )) as HTMLTextAreaElement
    // The section remounts per version, so this box is the fresh v2 control.
    fireEvent.change(box, { target: { value: "x".repeat(2001) } })
    const rerun = await screen.findByRole("button", {
      name: "Request rewrite",
    })
    expect(rerun.hasAttribute("disabled")).toBe(true)
    expect(seen).toHaveLength(1)
  })
})
