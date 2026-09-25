import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react"
import {
  getSession,
  isUnauthenticated,
  login,
  logout,
  type Session,
} from "./client"

export function sessionExpired(session: Session, now: number): boolean {
  return now >= Date.parse(session.expiresAt)
}

export function sessionMessage(cause: unknown): string {
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

export interface SessionState {
  session: Session | null | undefined
  error: string | null
  signingIn: boolean
  signingOut: boolean
  signIn: (password: string) => Promise<void>
  signOut: () => Promise<void>
  loseSession: () => void
}

const SessionContext = createContext<SessionState | null>(null)

export function useSession(): SessionState {
  const state = useContext(SessionContext)
  if (!state) throw new Error("useSession must be used inside SessionProvider")
  return state
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null | undefined>(undefined)
  const [error, setError] = useState<string | null>(null)
  const [signingIn, setSigningIn] = useState(false)
  const [signingOut, setSigningOut] = useState(false)

  const loseSession = useCallback(() => {
    setSession(null)
    setError(null)
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    getSession(controller.signal)
      .then(setSession)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(sessionMessage(cause))
      })
    return () => controller.abort()
  }, [])

  useEffect(() => {
    if (!session) return
    const expiresAt = Date.parse(session.expiresAt)
    const check = () => {
      if (Date.now() >= expiresAt) loseSession()
    }
    check()
    const timer = window.setTimeout(check, Math.max(0, expiresAt - Date.now()))
    window.addEventListener("focus", check)
    document.addEventListener("visibilitychange", check)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener("focus", check)
      document.removeEventListener("visibilitychange", check)
    }
  }, [session, loseSession])

  const signIn = useCallback(async (password: string) => {
    setSigningIn(true)
    setError(null)
    try {
      setSession(await login(password))
    } catch (cause) {
      setError(sessionMessage(cause))
      throw cause
    } finally {
      setSigningIn(false)
    }
  }, [])

  const signOut = useCallback(async () => {
    if (!session) return
    setSigningOut(true)
    setError(null)
    try {
      await logout(session.csrfToken)
      loseSession()
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setError(sessionMessage(cause))
    } finally {
      setSigningOut(false)
    }
  }, [session, loseSession])

  const value = useMemo<SessionState>(
    () => ({ session, error, signingIn, signingOut, signIn, signOut, loseSession }),
    [session, error, signingIn, signingOut, signIn, signOut, loseSession]
  )
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
