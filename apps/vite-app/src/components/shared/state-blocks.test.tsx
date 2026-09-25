import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  PausedBlock,
  UnsupportedBlock,
} from "@/components/shared/state-blocks";

describe("LoadingBlock", () => {
  it("announces the passed-in label as a live status", () => {
    const html = renderToStaticMarkup(
      <LoadingBlock label="Loading your search…" />,
    );
    expect(html).toContain('role="status"');
    expect(html).toContain("Loading your search…");
  });
});

describe("ErrorBlock", () => {
  it("renders the passed-in message as an alert with a retry action", () => {
    const html = renderToStaticMarkup(
      <ErrorBlock
        title="Search failed"
        message="The server returned 500."
        onRetry={() => {}}
        retryLabel="Retry search"
      />,
    );
    expect(html).toContain('role="alert"');
    expect(html).toContain("Search failed");
    expect(html).toContain("The server returned 500.");
    expect(html).toContain("Retry search");
    expect(html).toContain("<button");
  });

  it("renders no action when no retry handler is passed", () => {
    const html = renderToStaticMarkup(
      <ErrorBlock message="The server returned 500." />,
    );
    expect(html).not.toContain("<button");
  });

  it("accepts a retry handler without calling it during render", () => {
    const onRetry = vi.fn();
    renderToStaticMarkup(
      <ErrorBlock message="Failed." onRetry={onRetry} />,
    );
    expect(onRetry).not.toHaveBeenCalled();
  });
});

describe("EmptyBlock", () => {
  it("renders only the passed-in copy and action", () => {
    const html = renderToStaticMarkup(
      <EmptyBlock
        title="No jobs yet"
        description="Run a search to collect listings."
        actionLabel="Go to search"
        onAction={() => {}}
      />,
    );
    expect(html).toContain("No jobs yet");
    expect(html).toContain("Run a search to collect listings.");
    expect(html).toContain("Go to search");
  });

  it("renders no button without both an action label and handler", () => {
    const withoutHandler = renderToStaticMarkup(
      <EmptyBlock title="No jobs yet" actionLabel="Go to search" />,
    );
    expect(withoutHandler).not.toContain("<button");
    const withoutLabel = renderToStaticMarkup(
      <EmptyBlock title="No jobs yet" onAction={() => {}} />,
    );
    expect(withoutLabel).not.toContain("<button");
  });
});

describe("PausedBlock", () => {
  it("renders the passed-in paused state and resume action", () => {
    const html = renderToStaticMarkup(
      <PausedBlock
        title="Search paused"
        message="Resume when you are ready."
        onResume={() => {}}
      />,
    );
    expect(html).toContain("Search paused");
    expect(html).toContain("Resume when you are ready.");
    expect(html).toContain("Resume");
  });
});

describe("UnsupportedBlock", () => {
  it("renders the passed-in unavailable message with no action", () => {
    const html = renderToStaticMarkup(
      <UnsupportedBlock
        title="Grouping unavailable"
        message="Personalized groups are not supported by the server yet."
      />,
    );
    expect(html).toContain("Grouping unavailable");
    expect(html).toContain(
      "Personalized groups are not supported by the server yet.",
    );
    expect(html).not.toContain("<button");
  });
});
