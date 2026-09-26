// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"
import {
  clearAnswerDraft,
  clearAnswerDraftsForCheck,
  clearAnswerDraftsForJob,
  getAnswerDraft,
  hasAnswerDraft,
  setAnswerDraft,
  useAnswerDraft,
  type AnswerDraftKey,
} from "@/components/shared/answer-drafts"

const boxA: AnswerDraftKey = {
  jobId: "job-1",
  checkId: "check-1",
  questionId: "q-1",
}
const boxB: AnswerDraftKey = {
  jobId: "job-1",
  checkId: "check-1",
  questionId: "q-2",
}

afterEach(() => {
  cleanup()
  clearAnswerDraftsForJob("job-1")
  clearAnswerDraftsForJob("job-2")
  vi.unstubAllGlobals()
})

function DraftProbe({ box, label }: { box: AnswerDraftKey; label: string }) {
  const { draft, setDraft, clearDraft } = useAnswerDraft(box)
  return (
    <div>
      <p>
        {label}: {draft === null ? "empty" : draft}
      </p>
      <button type="button" onClick={() => setDraft("owner wording")}>
        write {label}
      </button>
      <button type="button" onClick={clearDraft}>
        clear {label}
      </button>
    </div>
  )
}

describe("answer-draft preservation", () => {
  it("scopes drafts to the exact job, check and question", () => {
    setAnswerDraft(boxA, "first answer")
    expect(getAnswerDraft(boxA)).toBe("first answer")
    expect(hasAnswerDraft(boxA)).toBe(true)
    expect(getAnswerDraft(boxB)).toBeNull()
    expect(
      getAnswerDraft({ ...boxA, checkId: "check-2" })
    ).toBeNull()
    expect(getAnswerDraft({ ...boxA, jobId: "job-2" })).toBeNull()
  })

  it("preserves dirty text across unmount and return navigation", () => {
    setAnswerDraft(boxA, "typed before leaving")
    const first = render(<DraftProbe box={boxA} label="a" />)
    expect(screen.getByText("a: typed before leaving")).toBeDefined()
    first.unmount()
    cleanup()
    render(<DraftProbe box={boxA} label="a" />)
    expect(screen.getByText("a: typed before leaving")).toBeDefined()
  })

  it("updates boxes reactively without touching sibling questions", async () => {
    render(
      <>
        <DraftProbe box={boxA} label="a" />
        <DraftProbe box={boxB} label="b" />
      </>
    )
    expect(screen.getByText("a: empty")).toBeDefined()
    act(() => {
      setAnswerDraft(boxA, "owner wording")
    })
    await screen.findByText("a: owner wording")
    expect(screen.getByText("b: empty")).toBeDefined()
  })

  it("clears drafts per check or per job after commit", () => {
    setAnswerDraft(boxA, "one")
    setAnswerDraft(boxB, "two")
    clearAnswerDraftsForCheck("job-1", "check-1")
    expect(hasAnswerDraft(boxA)).toBe(false)
    expect(hasAnswerDraft(boxB)).toBe(false)

    setAnswerDraft(boxA, "one")
    setAnswerDraft({ ...boxA, checkId: "check-2" }, "other check")
    clearAnswerDraft(boxA)
    expect(hasAnswerDraft(boxA)).toBe(false)
    expect(
      getAnswerDraft({ ...boxA, checkId: "check-2" })
    ).toBe("other check")
    clearAnswerDraftsForJob("job-1")
    expect(
      getAnswerDraft({ ...boxA, checkId: "check-2" })
    ).toBeNull()
  })
})
