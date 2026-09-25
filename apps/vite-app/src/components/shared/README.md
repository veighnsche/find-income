# Shared shell components (Lane B · B1)

Reusable shadcn-based primitives for the Vite frontend. Every component renders
**only passed-in data**: no fetching, no simulated activity, no invented
percentages or live-work claims. Import from `@/components/shared/index` or
from the individual files below.

## `actor-label.tsx` — `ActorLabel`

Labels who performs a working stage. Jev is always labelled as a classifier,
never as an LLM.

```tsx
<ActorLabel actor="codex" /> // "codex" | "jev" | "owner"
```

| Prop | Type | Notes |
| ---- | ---- | ----- |
| `actor` | `"codex" \| "jev" \| "owner"` | Required. Codex renders icon + label; Jev renders "Jev · classifier". |
| `className` | `string` | Optional extra classes. |

## `shell-nav.tsx` — `ShellNav`

Primary navigation. Pass the real destinations; the active entry gets
`aria-current="page"`.

```tsx
<ShellNav
  ariaLabel="Primary"
  items={[
    { id: "search", label: "My search", href: "/search", active: true },
    { id: "jobs", label: "Jobs", href: "/jobs", badge: "4" },
    { id: "apps", label: "Applications", disabled: true },
  ]}
/>
```

`ShellNavItem`: `id` (required), `label` (required), `href` (omit for
non-navigable text), `active`, `disabled` (renders a non-link span, never
navigates), `badge` (count/status text shown beside the label). Native anchors
keep Tab/Enter/Screen-reader behavior; the list wraps on narrow screens and
long labels keep their full text in `title`.

## `stage-progress.tsx` — `StageProgress`, `SEVEN_STAGES`

Seven-stage progress display. Stages are progress, not page visits: this is a
static list, not navigation. Exactly one stage can be active because there is a
single `activeStageId` prop.

```tsx
<StageProgress
  activeStageId="check"
  stages={[
    { id: "goals", label: "Your goals", state: "complete" },
    { id: "check", label: "Check job details", state: "upcoming" },
  ]}
/>
```

| Prop | Type | Notes |
| ---- | ---- | ----- |
| `stages` | `StageInput[]` | Optional. Defaults to the seven journey labels, all `upcoming`. Each item: `id`, `label`, `state: "complete" \| "upcoming" \| "blocked"`. |
| `activeStageId` | `string \| null` | Optional. The one current step (`aria-current="step"`). An unknown id marks nothing current rather than guessing. |
| `ariaLabel` | `string` | Optional, defaults to `"Application progress"`. |

Screen readers hear "(current step) / (completed) / (blocked) / (not started)"
per stage. No percentages are rendered.

## `state-blocks.tsx` — status states

Each block renders only the copy it receives. Actions appear only when a
handler is passed, and handlers never run during render.

- `LoadingBlock({ label, detail? })` — `role="status"` live region with
  skeleton rows. `label` should describe what is actually loading.
- `ErrorBlock({ title?, message, onRetry?, retryLabel? })` — destructive alert
  (`role="alert"`). Retry button only when `onRetry` is passed.
- `EmptyBlock({ title, description?, actionLabel?, onAction? })` — empty state.
  Button only when both `actionLabel` and `onAction` are passed.
- `PausedBlock({ title?, message, onResume?, resumeLabel? })` — paused state
  with optional resume action.
- `UnsupportedBlock({ title?, message })` — honest "not available yet" state,
  never an action.

## `activity-disclosure.tsx` — `ActivityDisclosure`

The single collapsible activity feed for Codex/Jev/owner working stages. Feed
it observed events only: actions, results, source links, blockers.

```tsx
<ActivityDisclosure
  actor="codex"
  phase="Find jobs · collection"
  status="Finished: 4 kept, 1 blocker"
  entries={[
    { id: "a1", kind: "action", text: "Queried the public listings API" },
    { id: "r1", kind: "result", text: "Kept 4 of 9 collected listings" },
    { id: "s1", kind: "source", text: "Example Careers", href: "https://example.com/careers" },
    { id: "b1", kind: "blocker", text: "Career page blocked the check" },
  ]}
/>
```

| Prop | Type | Notes |
| ---- | ---- | ----- |
| `actor` | `"codex" \| "jev" \| "owner"` | Required. Shown via `ActorLabel`. |
| `phase` | `string` | Required. Caller-supplied phase label, e.g. `"Find jobs · collection"`. |
| `status` | `string` | Required. Caller-supplied truthful status/count text, rendered verbatim. The component adds no "live" wording. |
| `entries` | `ActivityEntry[]` | Required. `{ id, kind: "action" \| "result" \| "source" \| "blocker", text, href? }`. Entries with `href` render as links. |
| `defaultOpen` / `open` / `onOpenChange` | `boolean` / `boolean` / `(open) => void` | Optional uncontrolled/controlled open state. |
| `emptyText` | `string` | Optional honest empty text, defaults to `"No activity recorded yet."`. |

The header shows the real entry count derived from `entries.length`. The
trigger is a native button (Tab to focus, Enter/Space to toggle,
`aria-expanded` handled by the collapsible). Long feeds scroll inside a
`max-h-64` region; text wraps instead of clipping.

## Rules for feature lanes

- Pass real server state and observed events; never invent activity, counts, or
  percentages to fill these components.
- Wording that claims liveness ("Live", "Working now…") must come from real
  run state in the feature lane, not from these primitives.
- `status`/`message` strings are rendered verbatim: keep them truthful.
