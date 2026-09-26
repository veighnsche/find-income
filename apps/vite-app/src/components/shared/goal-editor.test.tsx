import { afterEach, describe, expect, it, vi } from "vitest"
import {
  hasGoalEditorOpener,
  registerGoalEditorOpener,
  requestGoalEditorOpen,
} from "@/components/shared/goal-editor"

afterEach(() => {
  registerGoalEditorOpener(null)
})

describe("goal-editor open/focus callback", () => {
  it("reports false when no editor is mounted", () => {
    expect(hasGoalEditorOpener()).toBe(false)
    expect(requestGoalEditorOpen()).toBe(false)
  })

  it("opens and focuses the registered editor with the caller reason", () => {
    const open = vi.fn()
    registerGoalEditorOpener(open)
    expect(hasGoalEditorOpener()).toBe(true)
    expect(requestGoalEditorOpen("change-goals")).toBe(true)
    expect(open).toHaveBeenCalledWith("change-goals")
  })

  it("clears the callback when the editor unmounts", () => {
    const unregister = registerGoalEditorOpener(() => {})
    expect(hasGoalEditorOpener()).toBe(true)
    unregister()
    expect(hasGoalEditorOpener()).toBe(false)
    expect(requestGoalEditorOpen()).toBe(false)
  })

  it("keeps the newest registration when editors replace each other", () => {
    const first = vi.fn()
    const second = vi.fn()
    const unregisterFirst = registerGoalEditorOpener(first)
    registerGoalEditorOpener(second)
    unregisterFirst()
    expect(requestGoalEditorOpen()).toBe(true)
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
  })
})
