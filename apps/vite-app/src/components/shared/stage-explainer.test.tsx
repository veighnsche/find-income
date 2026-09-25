// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import { StageExplainer } from "@/components/shared/stage-explainer"

afterEach(cleanup)

describe("StageExplainer", () => {
  it("explains the check stage without inventing questions", () => {
    render(<StageExplainer stage="check" />)
    expect(
      screen.getByRole("heading", { name: "Checking the jobs you chose" })
    ).toBeDefined()
    expect(screen.getByText(/won't invent questions/i)).toBeDefined()
  })

  it("explains the answer stage with Jev and blanks", () => {
    render(<StageExplainer stage="answer" />)
    expect(
      screen.getByRole("heading", { name: "Answer the employer's questions" })
    ).toBeDefined()
    expect(screen.getByText(/leave it blank/i)).toBeDefined()
    expect(screen.getByText(/no language model/i)).toBeDefined()
  })

  it("explains the prepare stage with review-before-send", () => {
    render(<StageExplainer stage="prepare" />)
    expect(
      screen.getByRole("heading", { name: "Putting your application together" })
    ).toBeDefined()
    expect(
      screen.getByText(/until you review it and choose to send/i)
    ).toBeDefined()
  })
})
