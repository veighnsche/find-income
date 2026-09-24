# Private Codex protocol client (T38 slice)

This package is a stdlib-only App Server client for the pinned `codex-cli 0.153.4` protocol. It starts no process, reads no environment/configuration, opens no connection and runs no login or model turn by itself. The application runtime now calls its typed methods for an explicitly commissioned round turn, while deployment and live runner verification remain open. There is no HTTP/raw-RPC proxy in this package.

Compatibility evidence was generated locally from 0.153.4 and recorded in the project's `implementation-notes/codex/` handoff. `PinnedVersion` and `SchemaSHA256` identify that baseline; the initialize user-agent check is not executable attestation. The supervisor must verify the selected platform binary separately. The [official App Server reference](https://learn.chatgpt.com/docs/app-server) is a cross-check; it can describe capabilities absent from this binary.

## Transport and lifecycle contract

`NewClient` takes an injected `Transport` (`io.Reader`, `io.Writer`, `io.Closer`) and bounded options. **Transport.Close must promptly unblock both Read AND Write**, and must be safe while either is running. A pipe wrapper that closes only stdout is insufficient. `Client.Close` closes transport and waits for its two goroutines; it cannot make a nonconforming transport cancellable. Transport errors are reduced to safe sentinels rather than exposing arbitrary error strings.

The future supervisor creates and owns the transport, verifies the binary/state directory and runner isolation, and terminates/reaps the whole owned process group on exit. Closing this client/SSH stream alone does not establish process cleanup. The accepted direction is a separate private runner; restricted SSH is still a transport proposal. This package does not select or enable a deployment.

Call `Initialize` once. It verifies the returned 0.153.4 user-agent, sends `initialized`, then enables typed methods. Failed initialization requires a new client. Each instance has a distinct generation; do not attach a new process to an existing client. Read `Done`/`Err` for terminal connection status and consume the single ordered `Events` stream promptly. `Close` is idempotent.

One goroutine reads newline-delimited frames; one writes. Defaults are 1 MiB/frame (excluding newline), 32 pending calls and 32 pending server requests, 64 queued outbound frames and 64 queued events, 15-second request/write timeout, and five-minute local approval TTL. Options have hard bounds. At most one additional frame is actively being written. Backpressure on a new call rejects it; event/server-request overload fails the connection so lifecycle facts cannot silently disappear. There is no unbounded event history or per-call worker goroutine in this package.

Cancellation/deadline after a completed write releases the pending call. A late/duplicate/unknown response is counted and ignored, never replayed or applied to a newer call. Cancellation while writing closes the connection because the frame may be partial. Every writer operation, including an unsupported-server-request reply, also has a timeout. All uncertain dispatch outcomes require service reconciliation; **timeout does not mean the remote operation did not happen**. EOF, malformed/oversized frames, and transport failure fail pending calls. No reconnect or automatic retry occurs here.

## Narrow method and event boundary

Exported methods cover managed device/browser login, cancel/logout, account/limits/models, MCP discovery and typed tool dispatch, supported history reads, typed thread/text-turn creation, steering, interruption, and bounded server-request replies. No public generic call method exists. Command execution, filesystem APIs, external tokens, provider configuration, purchases/resets, and outbound messaging are absent. `StartThread` accepts trusted application instructions and fixes `approvalPolicy=never`; the runner configuration selects the named native permission profile. `StartTurn` accepts text only. Neither exposes arbitrary runtime configuration. `ListThreadItems` returns `ErrUnsupported` without writing: 0.153.4 advertised it in schema but rejected it in the real probe. `ResumeThread` loads a persisted materialized thread on a new connection; `ReadThread` reads metadata only (`includeTurns=false`, since full hydration is deprecated and hangs); `ListTurns` with `itemsView=full` is the authoritative history read, and `ObserveTurn` resumes once when the first page reports the thread unavailable. Resume/turn-list/steer/interrupt/tool-call shapes are verified against the real 0.153.4 binary with an unauthenticated isolated home; model-driven turns still require owner sign-in. Two timing rules from the T02 proof: observe a turn via `ListTurns` before interrupting (an interrupt that races turn attach is rejected with -32600), and `ResumeThread` on a new connection before starting work (observation alone leaves the thread unloaded for writes).

**All method results and events remain internal, untrusted wire data. They are not browser DTOs.** A service must validate and project them before display or persistence. In particular, validate official login URL scheme/host and attempt correlation, account mode, model/effort availability, history completeness/status, question IDs/options, event schemas and source attribution. Do not log or directly SSE-forward `LoginAttempt`, `Event.Params`, server request params, MCP input schemas, or thread items. Login codes/URLs belong only in the owner-only short-lived connection flow. No plaintext response/error logging exists in this package. `RPCError` retains only its numeric code; diagnostic counters retain no payloads.

Known event method names are allowed through in wire order; their bodies receive only the validation needed for local pending-request invalidation. Unknown notifications are counted and dropped. Malformed envelopes, duplicate top-level fields, ambiguous response/error objects, and invalid ID types fail the connection. String and integer request IDs remain distinct and preserve integer precision.

## Native request contract

Command/file approvals and user-input questions produce opaque `RequestToken`s at the low-level protocol boundary. Tokens are bound to connection generation, request occurrence and exact wire ID. The same ID reused later gets a different token. Local expiry, `serverRequest/resolved`, turn completion/new-turn notifications, connection failure and a reply invalidate them. The round controller rejects all native approval and input requests with a fixed error, interrupts the turn and leaves the outcome uncertain. No dashboard approval or native question route is exposed.

The writer rechecks a queued token immediately before dispatch; a resolution processed before that point prevents the reply. A reply already in flight cannot be recalled. Repeated/concurrent replies cannot send a second authorization. Failed or uncertain reply writes close the connection and are not automatically retried. The fixed rejection path carries no user-supplied answer or authorization.

Unsupported server requests—including permission expansion, external-token refresh, dynamic tools and MCP elicitation—receive a fixed `-32601` response. Nothing is auto-approved. Unknown requests are bounded by the same writer queue and timeout.

## Next integration contracts

1. **Supervisor (remaining T38):** injected closeable transport, pinned per-platform artifact, minimal child environment, dedicated state/work paths, verified runner sandbox, whole-process-group shutdown/reaping, startup health and failure reporting. Match returned home/platform to the intended runner. Never pass SQLite/backups, TYPESAFE_API_KEY, API keys or admin credentials into that runner.
2. **Account service (T39):** owner authentication/CSRF, serialized official login attempts, URL validation, cancellation/completion correlation, account/model/limit validation and redacted public DTOs. Missing quota remains unavailable. No API-key fallback.
3. **Run service (T40):** the application now persists dispatch intent and exact remote IDs for an explicitly called round turn, bounds one active turn, binds scoped MCP identity and retains uncertain history for reconciliation. Authenticated production history and event replay remain unverified. Do not interpret transport acknowledgement as a completed turn.
4. **Execution gate:** tools stay unavailable until selected-host isolation, required production MCP behavior, owner login, supported history and live turn interruption/crash recovery are verified. Offline client tests do not satisfy these gates.

## Verification

### Bound turn controller

`NewTurnController(client, instructions, model, effort)` owns one client's event stream during a previously persisted round dispatch. Its `Run(ctx, text, hooks)` requires `BindThread` and `BindTurn` callbacks that record the exact returned IDs before the next remote step; `RunResumed(ctx, threadID, text, hooks)` starts a turn on a thread already loaded on the connection and needs only `BindTurn`. The application reserves and marks the round attempt dispatched before invoking it; this package never creates that authority itself. It accepts only an exact thread/turn terminal notification. Raw model prose, an unrelated completion, and a wire acknowledgement do not establish a saved record. `hooks.OnItem` observes item/started and item/completed for the running turn; message deltas stay dropped by design. Cancellation, missing identifiers, a rejected native request or transport loss interrupt when possible, close the client and leave the result uncertain for exact-history reconciliation. It never retries the turn.

`codexservice.ExecuteRoundTurn` supplies the durable callbacks and a generation-bound tool capability. Its route remains unmounted; no idle login or status call starts a turn. Synthetic tests do not satisfy the private runner's live verification gates.

Run from `apps/api`, with writable caches:

```sh
GOCACHE=/private/tmp/jobseek-go-cache GOMODCACHE=/private/tmp/jobseek-go-mod go test -race -count=1 ./internal/codex
GOCACHE=/private/tmp/jobseek-go-cache GOMODCACHE=/private/tmp/jobseek-go-mod go vet ./internal/codex
```

Tests use `net.Pipe` and synthetic frames only. They cover handshake/version rejection, out-of-order and unknown/duplicate IDs, numeric/string ID distinctions, timeout/cancellation, blocked writer cleanup, pending/event/write capacity, malformed/oversized/truncated frames, EOF failure, approval generation/reuse/expiry/queued resolution, unsupported requests, typed wire shapes and safe error text. They do not start Codex, log into an account, call a provider or contact a remote host.
