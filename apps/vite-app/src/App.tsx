import { useState, type FormEvent } from "react"
import {
  getActiveRound,
  getMuseReadiness,
  getPreferences,
  getSourcedOwnerContext,
  isCareerSourceNotConnected,
  listRoleWorkflows,
  type MuseReadiness,
  type Preferences,
  type RoleWorkflowState,
  type Round,
  type SourcedOwnerContext,
} from "@/api/client"
import { SessionProvider, useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  ShellNav,
} from "@/components/shared"
import { journeyLabelFor } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useRoute, type Route } from "@/routes/useRoute"
import { AnswersPage } from "@/features/answers"
import { CheckPage } from "@/features/check"
import { HandoffPage } from "@/features/handoff"
import { PreparePage } from "@/features/prepare"
import { ApplicationsPage } from "@/pages/ApplicationsPage"
import { JobDetailPage } from "@/pages/JobDetailPage"
import { JobsPage } from "@/pages/JobsPage"
import { SearchPage } from "@/pages/SearchPage"
import { TodayPage } from "@/pages/TodayPage"

export function App() {
  return (
    <SessionProvider>
      <Shell />
    </SessionProvider>
  )
}

function Shell() {
  const { session, error } = useSession()
  const [route, navigate] = useRoute()

  if (session === undefined) {
    if (error !== null)
      return (
        <div className="mx-auto flex min-h-svh w-full max-w-2xl flex-col justify-center p-6">
          <ErrorBlock
            title="Could not reach the dashboard"
            message={error}
            retryLabel="Reload"
            onRetry={() => window.location.reload()}
          />
        </div>
      )
    return (
      <div className="mx-auto flex min-h-svh w-full max-w-2xl flex-col justify-center p-6">
        <LoadingBlock label="Checking dashboard session…" />
      </div>
    )
  }

  if (session === null) return <SignInPanel />

  return <Frame route={route} navigate={navigate} sessionExpiresAt={session.expiresAt} />
}

function Frame({
  route,
  navigate,
  sessionExpiresAt,
}: {
  route: Route
  navigate: (route: Route) => void
  sessionExpiresAt: string
}) {
  const activeRound = useRead(
    "shell-active-round",
    (signal) => getActiveRound(signal),
    { scopes: ["run"] }
  )
  const workflows = useRead(
    "shell-role-workflows",
    (signal) => listRoleWorkflows(signal),
    { scopes: ["selection", "workflows"] }
  )
  const goals = useRead("shell-goals", (signal) => getPreferences(signal), {
    scopes: ["goals"],
  })
  // Contributor readiness has no invalidation scope of its own; it reads
  // once per mount and only gates the Ready claim when positively known.
  const contributor = useRead("shell-contributor", (signal) =>
    getMuseReadiness("contributor", signal)
  )
  const chosen =
    workflows.status === "ready" ? workflows.data.length > 0 : false
  const navItems = [
    {
      id: "today",
      label: "Today",
      href: "#/today",
      active: route.page === "today",
    },
    {
      id: "search",
      label: "My search",
      href: "#/search",
      active: route.page === "search",
    },
    {
      id: "jobs",
      label: "Jobs",
      href: "#/jobs",
      active:
        route.page === "jobs" ||
        route.page === "check" ||
        route.page === "answers" ||
        route.page === "prepare" ||
        route.page === "handoff",
    },
  ]
  if (chosen) {
    navItems.push({
      id: "applications",
      label: "Applications",
      href: "#/applications",
      active: route.page === "applications",
    })
  }

  return (
    <div className="mx-auto flex min-h-svh w-full max-w-6xl flex-col gap-4 p-4 sm:p-6 md:flex-row md:gap-6">
      <aside className="flex min-w-0 flex-col gap-4 md:w-60 md:shrink-0">
        <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <p className="font-heading text-lg font-semibold">Find income</p>
            <p className="text-xs text-muted-foreground">
              Your personal recruitment agency
            </p>
          </div>
          <div className="md:hidden">
            <SignOutButton />
          </div>
        </div>
        <ShellNav ariaLabel="Primary" items={navItems} direction="vertical" />
        <OwnerIdentityFoot sessionExpiresAt={sessionExpiresAt} />
        <div className="hidden md:block">
          <SignOutButton />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-2xl border bg-card px-4 py-2">
          <p className="text-sm font-medium">Your job search</p>
          <WorkStatusText
            activeRound={activeRound}
            workflows={workflows}
            goals={goals}
            contributor={contributor}
          />
        </div>

        <main className="min-w-0 flex-1">
          <RoutePage route={route} navigate={navigate} />
        </main>
      </div>
    </div>
  )
}

// The strip names the real step and its real state (F2/R14): an active
// round reports its Find-jobs state, chosen work reports per-stage role
// counts on the seven-step journey, and Ready is claimed only with saved
// goals, no running work and no known Contributor blocker.
function WorkStatusText({
  activeRound,
  workflows,
  goals,
  contributor,
}: {
  activeRound: { status: string; data: Round | null }
  workflows: { status: string; data: RoleWorkflowState[] | null }
  goals: { status: string; data: Preferences | null }
  contributor: { status: string; data: MuseReadiness | null }
}) {
  if (
    activeRound.status === "loading" ||
    workflows.status === "loading" ||
    goals.status === "loading" ||
    contributor.status === "loading"
  )
    return <p className="text-xs text-muted-foreground">Checking work status…</p>
  if (
    activeRound.status !== "ready" ||
    workflows.status !== "ready" ||
    goals.status !== "ready"
  )
    return <p className="text-xs text-muted-foreground">Work status unavailable</p>
  const round = activeRound.data
  if (round !== null) {
    return (
      <p className="text-xs text-muted-foreground">
        {describeActiveRound(round.state)}
      </p>
    )
  }
  const items = workflows.data ?? []
  if (items.length > 0) {
    return (
      <p className="text-xs text-muted-foreground">
        {describeChosenWork(items)}
      </p>
    )
  }
  const criteria = goals.data?.roleCriteria.length ?? 0
  if (criteria === 0)
    return (
      <p className="text-xs text-muted-foreground">No search goals saved yet</p>
    )
  if (contributor.status === "ready" && contributor.data !== null) {
    const readiness = contributor.data
    if (readiness.state !== "ready") {
      const why =
        readiness.detail !== "" ? readiness.detail : readiness.code
      return (
        <p className="text-xs text-muted-foreground">
          {`Find jobs unavailable: ${why}`}
        </p>
      )
    }
  }
  // A failed readiness read leaves the blocker unknown rather than
  // inventing one; the Find jobs panel itself reports the exact blocker.
  return <p className="text-xs text-muted-foreground">Ready to find jobs</p>
}

function describeActiveRound(state: string): string {
  switch (state) {
    case "queued":
      return "Find jobs · queued"
    case "running":
      return "Find jobs · running"
    case "awaiting_input":
      return "Find jobs · needs your input"
    case "stopping":
      return "Find jobs · stopping"
    case "paused":
      return "Find jobs · paused"
    default:
      return `Find jobs · ${state}`
  }
}

function describeChosenWork(items: RoleWorkflowState[]): string {
  const handoff = items.filter((item) => item.stage === "handoff_saved").length
  if (handoff === items.length)
    return `Handoff · ${items.length} saved ${items.length === 1 ? "role" : "roles"}`
  const counts = new Map<string, number>()
  const order: string[] = []
  for (const item of items) {
    // A mixed list names Handoff roles honestly instead of letting the
    // null journey label fall through to Blocked.
    const label =
      item.stage === "handoff_saved"
        ? "Handoff"
        : (journeyLabelFor(item.stage) ?? "Blocked")
    if (!counts.has(label)) order.push(label)
    counts.set(label, (counts.get(label) ?? 0) + 1)
  }
  return order
    .map((label) => {
      const count = counts.get(label) ?? 0
      return `${label} · ${count} ${count === 1 ? "role" : "roles"}`
    })
    .join("; ")
}

// Sidebar foot (F2/F01): the supported D4 owner identity, falling back to
// the session text while loading, on errors, or on servers that predate
// the owner-context route. GET-only like every other shell read.
function OwnerIdentityFoot({
  sessionExpiresAt,
}: {
  sessionExpiresAt: string
}) {
  const context = useRead(
    "shell-owner-identity",
    (signal) =>
      getSourcedOwnerContext(signal).catch((cause: unknown) => {
        if (isCareerSourceNotConnected(cause)) return null
        throw cause
      }) as Promise<SourcedOwnerContext | null>,
    { scopes: ["goals"] }
  )
  if (context.status === "ready" && context.data !== null) {
    const owner = context.data.owner
    return (
      <p
        className="mt-auto hidden text-xs text-muted-foreground md:block"
        title={`${owner.kind} ${owner.id} · session ends ${sessionExpiresAt}`}
      >
        Signed in as {owner.id}
      </p>
    )
  }
  return (
    <p
      className="mt-auto hidden text-xs text-muted-foreground md:block"
      title={sessionExpiresAt}
    >
      Signed in · session ends {sessionExpiresAt.slice(0, 10)}
    </p>
  )
}

function RoutePage({
  route,
  navigate,
}: {
  route: Route
  navigate: (route: Route) => void
}) {
  switch (route.page) {
    case "today":
      return <TodayPage />
    case "search":
      return <SearchPage />
    case "jobs":
      return route.jobId === null ? (
        <JobsPage />
      ) : (
        <JobDetailPage jobId={route.jobId} />
      )
    case "applications":
      return <ApplicationsPage jobId={route.jobId} />
    case "check":
      return <CheckPage jobId={route.jobId} />
    case "answers":
      return <AnswersPage jobId={route.jobId} />
    case "prepare":
      return <PreparePage jobId={route.jobId} />
    case "handoff":
      return <HandoffPage jobId={route.jobId} />
    case "not-found":
      return (
        <EmptyBlock
          title="Page not found"
          description={`The dashboard has no page for "${route.hash}".`}
          actionLabel="Open Today"
          onAction={() => navigate({ page: "today" })}
        />
      )
  }
}

function SignInPanel() {
  const { signIn, signingIn, error } = useSession()
  const [password, setPassword] = useState("")

  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    void signIn(password).catch(() => {
      // The failure is already in session error state, shown below.
    })
  }

  return (
    <div className="mx-auto flex min-h-svh w-full max-w-md flex-col justify-center p-6">
      <Card>
        <CardHeader>
          <CardTitle>Sign in</CardTitle>
          <CardDescription>
            The dashboard needs the owner password before it shows anything.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="flex min-w-0 flex-col gap-3">
            <div className="flex min-w-0 flex-col gap-2">
              <Label htmlFor="b2-password">Password</Label>
              <Input
                id="b2-password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                disabled={signingIn}
              />
            </div>
            {error !== null ? (
              <p role="alert" className="text-sm wrap-break-word text-destructive">
                {error}
              </p>
            ) : null}
            <Button
              type="submit"
              disabled={signingIn || password.length === 0}
            >
              {signingIn ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}

function SignOutButton() {
  const { signOut, signingOut } = useSession()
  return (
    <Button
      variant="outline"
      size="sm"
      disabled={signingOut}
      onClick={() => {
        void signOut()
      }}
    >
      {signingOut ? "Signing out…" : "Sign out"}
    </Button>
  )
}

export default App
