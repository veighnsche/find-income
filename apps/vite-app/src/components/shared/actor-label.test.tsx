import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ActorLabel } from "@/components/shared/actor-label";

describe("ActorLabel", () => {
  it("labels Codex with an icon and text", () => {
    const html = renderToStaticMarkup(<ActorLabel actor="codex" />);
    expect(html).toContain("Codex");
    expect(html).toContain("<svg");
  });

  it("labels Jev as a classifier and never as an LLM", () => {
    const html = renderToStaticMarkup(<ActorLabel actor="jev" />);
    expect(html).toContain("Jev");
    expect(html).toContain("classifier");
    expect(html.toLowerCase()).not.toContain("llm");
  });

  it("labels the owner without model wording", () => {
    const html = renderToStaticMarkup(<ActorLabel actor="owner" />);
    expect(html).toContain("Owner");
    expect(html.toLowerCase()).not.toContain("llm");
  });
});
