import { useState, type FormEvent } from "react"
import { SessionProvider, useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  ShellNav,
} from "@/components/shared"
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
import { AttemptOutcomeView } from "@/features/attempts"
import { CheckPage } from "@/features/check"
import { PreparePage } from "@/features/prepare"
import { ReviewPage } from "@/features/review/ReviewPage"
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

  return (
    <div className="mx-auto flex min-h-svh w-full max-w-4xl flex-col gap-5 p-4 sm:p-6">
      <header className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border bg-card px-4 py-3">
        <div className="min-w-0">
          <p className="font-heading text-lg font-semibold">Find income</p>
          <p
            className="text-xs text-muted-foreground"
            title={session.expiresAt}
          >
            Signed in · session ends {session.expiresAt.slice(0, 10)}
          </p>
        </div>
        <SignOutButton />
      </header>

      <ShellNav
        ariaLabel="Primary"
        items={[
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
          {
            id: "applications",
            label: "Applications",
            href: "#/applications",
            active:
              route.page === "applications" ||
              route.page === "review" ||
              route.page === "attempt",
          },
        ]}
      />

      <main className="min-w-0 flex-1">
        <RoutePage route={route} navigate={navigate} />
      </main>
    </div>
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
    case "review":
      return <ReviewPage jobId={route.jobId} />
    case "attempt":
      return <AttemptOutcomeView jobId={route.jobId} reviewId={route.reviewId} />
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
