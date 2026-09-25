// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { ResearchActivityEvent } from "@/api/client"
import {
  CheckActivityFeed,
  checkEntryKindFor,
  isCheckBlockerEvent,
  toCheckActivityEntry,
} from "@/features/check/check-activity-feed"

afterEach(() => {
  cleanup()
})

function eventFixture(
  eventId: string,
  kind: string,
  summary: string,
  refs?: ResearchActivityEvent["refs"]
): ResearchActivityEvent {
  return {
    eventId,
    at: "2026-09-20T10:00:00Z",
    kind,
    phase: "collect",
    summary,
    refs,
  }
}

describe("check activity mapping", () => {
  it("flags attention kinds as blockers", () => {
    expect(
      isCheckBlockerEvent(eventFixture("e1", "run.uncertain", "Uncertain"))
    ).toBe(true)
    expect(
      checkEntryKindFor(eventFixture("e1", "run.uncertain", "Uncertain"))
    ).toBe("blocker")
  })

  it("maps capture refs to sources and settled kinds to results", () => {
    expect(
      checkEntryKindFor(
        eventFixture("e1", "capture", "Captured", { captureId: "cap-1" })
      )
    ).toBe("source")
    expect(
      checkEntryKindFor(eventFixture("e2", "observation", "Observed"))
    ).toBe("result")
    expect(
      checkEntryKindFor(eventFixture("e3", "run.dispatched", "Dispatched"))
    ).toBe("action")
  })

  it("keeps refs visible as bare ids without inventing links", () => {
    const entry = toCheckActivityEntry(
      eventFixture("e1", "capture", "Captured posting", {
        captureId: "cap-1",
        assessmentId: "as-1",
        recordId: "rec-1",
      })
    )
    expect(entry).toEqual({
      id: "e1",
      kind: "source",
      text: "Captured posting (capture cap-1, assessment as-1, record rec-1)",
    })
  })
})

describe("check activity feed", () => {
  it("renders entries with blocker counts", () => {
    render(
      <CheckActivityFeed
        events={[
          eventFixture("e1", "run.dispatched", "Dispatched check"),
          eventFixture("e2", "run.uncertain", "Route uncertain"),
        ]}
        nextCursor=""
        loadingMore={false}
        loadMoreError={null}
        onLoadMore={() => {}}
      />
    )
    expect(screen.getByText("Check job details · activity")).toBeDefined()
    expect(screen.getAllByText("2 recorded · 1 blockers").length).toBeGreaterThan(0)
    expect(
      screen.queryByRole("button", { name: "Load more activity" })
    ).toBeNull()
  })

  it("shows the empty state when nothing is recorded", () => {
    render(
      <CheckActivityFeed
        events={[]}
        nextCursor=""
        loadingMore={false}
        loadMoreError={null}
        onLoadMore={() => {}}
      />
    )
    expect(
      screen.getAllByText("No check activity recorded").length
    ).toBeGreaterThan(0)
  })

  it("paginates and reports load-more failures", () => {
    const onLoadMore = vi.fn()
    const rendered = render(
      <CheckActivityFeed
        events={[eventFixture("e1", "run.dispatched", "Dispatched check")]}
        nextCursor="cursor-2"
        loadingMore={false}
        loadMoreError={null}
        onLoadMore={onLoadMore}
      />
    )
    fireEvent.click(screen.getByRole("button", { name: "Load more activity" }))
    expect(onLoadMore).toHaveBeenCalledTimes(1)
    rendered.rerender(
      <CheckActivityFeed
        events={[eventFixture("e1", "run.dispatched", "Dispatched check")]}
        nextCursor="cursor-2"
        loadingMore={false}
        loadMoreError="Timed out."
        onLoadMore={onLoadMore}
      />
    )
    expect(screen.getByText("Could not load more activity")).toBeDefined()
    expect(screen.getByText("Timed out.")).toBeDefined()
  })
})
