import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ActivityDisclosure } from "@/components/shared/activity-disclosure";

const ENTRIES = [
  { id: "e1", kind: "action" as const, text: "Queried the public listings API" },
  { id: "e2", kind: "result" as const, text: "Kept 4 of 9 collected listings" },
  {
    id: "e3",
    kind: "source" as const,
    text: "Example Careers",
    href: "https://example.com/careers",
  },
  { id: "e4", kind: "blocker" as const, text: "Career page blocked the check" },
];

describe("ActivityDisclosure", () => {
  it("renders actor, phase and the passed-in status verbatim", () => {
    const html = renderToStaticMarkup(
      <ActivityDisclosure
        actor="codex"
        phase="Find jobs · collection"
        status="Finished: 4 kept, 1 blocker"
        entries={[]}
        defaultOpen
      />,
    );
    expect(html).toContain("Codex");
    expect(html).toContain("Find jobs · collection");
    expect(html).toContain("Finished: 4 kept, 1 blocker");
  });

  it("uses a native button trigger so keyboard toggling works", () => {
    const html = renderToStaticMarkup(
      <ActivityDisclosure
        actor="jev"
        phase="Find jobs · classification"
        status="Saved 2 reasons"
        entries={ENTRIES}
        defaultOpen
      />,
    );
    expect(html).toContain("<button");
    expect(html).toContain("Jev");
    expect(html).toContain("classifier");
  });

  it("renders every passed-in entry with source links and blockers", () => {
    const html = renderToStaticMarkup(
      <ActivityDisclosure
        actor="codex"
        phase="Check job details"
        status="1 blocker recorded"
        entries={ENTRIES}
        defaultOpen
      />,
    );
    expect(html).toContain("Queried the public listings API");
    expect(html).toContain("Kept 4 of 9 collected listings");
    expect(html).toContain('href="https://example.com/careers"');
    expect(html).toContain("Career page blocked the check");
    expect(html).toContain("4 entries");
  });

  it("renders an honest empty state and a truthful zero count", () => {
    const html = renderToStaticMarkup(
      <ActivityDisclosure
        actor="owner"
        phase="Handoff"
        status="Nothing recorded"
        entries={[]}
        defaultOpen
      />,
    );
    expect(html).toContain("No activity recorded yet.");
    expect(html).toContain("0 entries");
  });

  it("keeps long feeds scrollable and invents no percentages or live claims", () => {
    const many = Array.from({ length: 50 }, (_, index) => ({
      id: `long-${index}`,
      kind: "action" as const,
      text: `Recorded action number ${index}`,
    }));
    const html = renderToStaticMarkup(
      <ActivityDisclosure
        actor="codex"
        phase="Find jobs · collection"
        status="Finished with 50 recorded actions"
        entries={many}
        defaultOpen
      />,
    );
    expect(html).toContain("Recorded action number 0");
    expect(html).toContain("Recorded action number 49");
    expect(html).toContain("50 entries");
    expect(html).toContain("overflow-y-auto");
    expect(html).not.toContain("%");
    expect(html.toLowerCase()).not.toContain("live");
  });
});
