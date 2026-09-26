import { useState, type FormEvent } from "react"
import {
  getActiveRound,
  listRoleWorkflows,
  type RoleWorkflowState,
  type Round,
} from "@/api/client"
import { SessionProvider, useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  ShellNav,
} from "@/components/shared"
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
  const activeRound = useRead("shell-active-round", (signal) =>
    getActiveRound(signal)
  )
  const workflows = useRead("shell-role-workflows", (signal) =>
    listRoleWorkflows(signal)
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
        route.page === "prepare",
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
        <p
          className="mt-auto hidden text-xs text-muted-foreground md:block"
          title={sessionExpiresAt}
        >
          Signed in · session ends {sessionExpiresAt.slice(0, 10)}
        </p>
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
          />
        </div>

        <main className="min-w-0 flex-1">
          <RoutePage route={route} navigate={navigate} />
        </main>
      </div>
    </div>
  )
}

function WorkStatusText({
  activeRound,
  workflows,
}: {
  activeRound: { status: string; data: Round | null }
  workflows: { status: string; data: RoleWorkflowState[] | null }
}) {
  if (activeRound.status === "loading" || workflows.status === "loading")
    return <p className="text-xs text-muted-foreground">Checking work status…</p>
  if (activeRound.status !== "ready" || workflows.status !== "ready")
    return <p className="text-xs text-muted-foreground">Work status unavailable</p>
  const round = activeRound.data
  if (round !== null) {
    switch (round.state) {
      case "paused":
        return <p className="text-xs text-muted-foreground">Search paused</p>
      case "awaiting_input":
        return <p className="text-xs text-muted-foreground">Search needs your input</p>
      case "stopping":
        return <p className="text-xs text-muted-foreground">Search stopping…</p>
      default:
        return <p className="text-xs text-muted-foreground">Search running…</p>
    }
  }
  const items = workflows.data ?? []
  if (items.length > 0) {
    const handoff = items.filter((item) =>
      ["reviewing", "sent"].includes(item.stage)
    ).length
    if (handoff === items.length)
      return <p className="text-xs text-muted-foreground">Applications ready for handoff</p>
    return (
      <p className="text-xs text-muted-foreground">
        {items.length} chosen {items.length === 1 ? "job" : "jobs"} in progress
      </p>
    )
  }
  return <p className="text-xs text-muted-foreground">Ready to find jobs</p>
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
