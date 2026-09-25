import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ShellNav } from "@/components/shared/shell-nav";

describe("ShellNav", () => {
  it("renders only the passed-in items with real links", () => {
    const html = renderToStaticMarkup(
      <ShellNav
        ariaLabel="Primary"
        items={[
          { id: "search", label: "My search", href: "/search" },
          { id: "jobs", label: "Jobs", href: "/jobs" },
        ]}
      />,
    );
    expect(html).toContain('aria-label="Primary"');
    expect(html).toContain('href="/search"');
    expect(html).toContain('href="/jobs"');
    expect(html).toContain("My search");
    expect(html).toContain("Jobs");
    expect(html).not.toContain("Applications");
  });

  it("marks the active item with aria-current and keeps it keyboard-focusable", () => {
    const html = renderToStaticMarkup(
      <ShellNav
        ariaLabel="Primary"
        items={[
          { id: "search", label: "My search", href: "/search", active: true },
        ]}
      />,
    );
    expect(html).toContain('aria-current="page"');
    expect(html).toContain("<a");
  });

  it("renders disabled items without a link target", () => {
    const html = renderToStaticMarkup(
      <ShellNav
        ariaLabel="Primary"
        items={[
          {
            id: "apps",
            label: "Applications",
            href: "/applications",
            disabled: true,
          },
        ]}
      />,
    );
    expect(html).toContain('aria-disabled="true"');
    expect(html).not.toContain("<a");
  });

  it("renders long labels in full without inventing entries", () => {
    const longLabel =
      "A very long navigation label that must remain fully readable for keyboard and screen reader users";
    const html = renderToStaticMarkup(
      <ShellNav
        ariaLabel="Primary"
        items={[{ id: "long", label: longLabel, href: "/long" }]}
      />,
    );
    expect(html).toContain(longLabel);
    expect(html).toContain(`title="${longLabel}"`);
  });
});
