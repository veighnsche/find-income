import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SEVEN_STAGES, StageProgress } from "@/components/shared/stage-progress";

describe("StageProgress", () => {
  it("renders the seven journey stages by default", () => {
    const html = renderToStaticMarkup(<StageProgress activeStageId="find" />);
    expect(SEVEN_STAGES).toHaveLength(7);
    for (const stage of SEVEN_STAGES) {
      expect(html).toContain(stage.label.replace("&", "&amp;"));
    }
  });

  it("marks exactly one stage as current", () => {
    const html = renderToStaticMarkup(<StageProgress activeStageId="check" />);
    expect(html.match(/aria-current="step"/g)).toHaveLength(1);
    expect(html).toContain("Check job details");
    expect(html).toContain("(current step)");
  });

  it("marks nothing current when the active id is unknown", () => {
    const html = renderToStaticMarkup(
      <StageProgress activeStageId="no-such-stage" />,
    );
    expect(html).not.toContain('aria-current="step"');
  });

  it("renders only passed-in stages and states", () => {
    const html = renderToStaticMarkup(
      <StageProgress
        activeStageId="b"
        stages={[
          { id: "a", label: "First", state: "complete" },
          { id: "b", label: "Second", state: "upcoming" },
          { id: "c", label: "Third", state: "blocked" },
        ]}
      />,
    );
    expect(html).toContain("First");
    expect(html).toContain("(completed)");
    expect(html).toContain("Third");
    expect(html).toContain("(blocked)");
    expect(html).not.toContain("Find jobs");
    expect(html.match(/aria-current="step"/g)).toHaveLength(1);
  });

  it("never renders invented percentages", () => {
    const html = renderToStaticMarkup(<StageProgress activeStageId="find" />);
    expect(html).not.toContain("%");
  });
});
