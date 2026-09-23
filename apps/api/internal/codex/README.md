# Private Codex protocol client (T38 slice)

This package is a stdlib-only App Server client for the pinned `codex-cli 0.153.4` protocol. It is not wired into the application. It starts no process, reads no environment/configuration, opens no connection and runs no login or model turn by itself. There is no HTTP/raw-RPC proxy, database integration, supervisor or deployment configuration. Full T38 remains open.

Compatibility evidence was generated locally from 0.153.4 and recorded in the project's `implementation-notes/codex/` handoff. `PinnedVersion` and `SchemaSHA256` identify that baseline; the initialize user-agent check is not executable attestation. The supervisor must verify the selected platform binary separately. The [official App Server reference](https://learn.chatgpt.com/docs/app-server) is a cross-check; it can describe capabilities absent from this binary.

## Transport and lifecycle contract

`NewClient` takes an injected `Transport` (`io.Reader`, `io.Writer`, `io.Closer`) and bounded options. **Transport.Close must promptly unblock both Read AND Write**, and must be safe while either is running. A pipe wrapper that closes only stdout is insufficient. `Client.Close` closes transport and waits for its two goroutines; it cannot make a nonconforming transport cancellable. Transport errors are reduced to safe sentinels rather than exposing arbitrary error strings.

The future supervisor creates and owns the transport, verifies the binary/state directory and runner isolation, and terminates/reaps the whole owned process group on exit. Closing this client/SSH stream alone does not establish process cleanup. The accepted direction is a separate private runner; restricted SSH is still a transport proposal. This package does not select or enable a deployment.

Call `Initialize` once. It verifies the returned 0.153.4 user-agent, sends `initialized`, then enables typed methods. Failed initialization requires a new client. Each instance has a distinct generation; do not attach a new process to an existing client. Read `Done`/`Err` for terminal connection status and consume the single ordered `Events` stream promptly. `Close` is idempotent.

One goroutine reads newline-delimited frames; one writes. Defaults are 1 MiB/frame (excluding newline), 32 pending calls and 32 pending server requests, 64 queued outbound frames and 64 queued events, 15-second request/write timeout, and five-minute local approval TTL. Options have hard bounds. At most one additional frame is actively being written. Backpressure on a new call rejects it; event/server-request overload fails the connection so lifecycle facts cannot silently disappear. There is no unbounded event history or per-call worker goroutine in this package.

Cancellation/deadline after a completed write releases the pending call. A late/duplicate/unknown response is counted and ignored, never replayed or applied to a newer call. Cancellation while writing closes the connection because the frame may be partial. Every writer operation, including an unsupported-server-request reply, also has a timeout. All uncertain dispatch outcomes require service reconciliation; **timeout does not mean the remote operation did not happen**. EOF, malformed/oversized frames, and transport failure fail pending calls. No reconnect or automatic retry occurs here.

## Narrow method and event boundary

Exported methods cover managed device/browser login, cancel/logout, account/limits/models, MCP discovery, supported history reads, typed thread/text-turn creation, interruption, and single-operation approval/question replies. No public generic call method exists. Steering, command execution, filesystem APIs, external tokens, provider configuration, purchases/resets, and outbound messaging are absent. `StartThread` accepts trusted application instructions with workspace-write/on-request/user settings; `StartTurn` accepts text only. Neither exposes arbitrary runtime configuration. `ListThreadItems` returns `ErrUnsupported` without writing: 0.153.4 advertised it in schema but rejected it in the real probe. Neither `ReadThread` nor `ListTurns` has been verified against authenticated saved history.

**All method results and events remain internal, untrusted wire data. They are not browser DTOs.** A service must validate and project them before display or persistence. In particular, validate official login URL scheme/host and attempt correlation, account mode, model/effort availability, history completeness/status, question IDs/options, event schemas and source attribution. Do not log or directly SSE-forward `LoginAttempt`, `Event.Params`, server request params, MCP input schemas, or thread items. Login codes/URLs belong only in the owner-only short-lived connection flow. No plaintext response/error logging exists in this package. `RPCError` retains only its numeric code; diagnostic counters retain no payloads.

Known event method names are allowed through in wire order; their bodies receive only the validation needed for local pending-request invalidation. Unknown notifications are counted and dropped. Malformed envelopes, duplicate top-level fields, ambiguous response/error objects, and invalid ID types fail the connection. String and integer request IDs remain distinct and preserve integer precision.

## Approval and question contract

Command/file approvals and user-input questions produce opaque `RequestToken`s. Tokens are bound to connection generation, request occurrence and exact wire ID. The same ID reused later gets a different token. Local expiry, `serverRequest/resolved`, turn completion/new-turn notifications, connection failure and a reply invalidate them. Tokens must stay in the trusted service's in-memory pending-request registry; they are not JSON-serializable credentials or durable approval records. After process/service restart all old dashboard approval handles must be invalidated.

The writer rechecks a queued token immediately before dispatch; a resolution processed before that point prevents the reply. A reply already in flight cannot be recalled. Repeated/concurrent replies cannot send a second authorization. Failed or uncertain reply writes close the connection and are not automatically retried. Only accept-once, decline and cancel decisions are allowed; no session grants or persistent policy amendments. The service must authenticate the owner, validate question answer IDs against the stored original request, apply expiry/operation authorization, and map its own opaque UI handle to a live token. This package does not provide those owner/persistence checks.

Unsupported server requests—including permission expansion, external-token refresh, dynamic tools and MCP elicitation—receive a fixed `-32601` response. Nothing is auto-approved. Unknown requests are bounded by the same writer queue and timeout.

## Next integration contracts

1. **Supervisor (remaining T38):** injected closeable transport, pinned per-platform artifact, minimal child environment, dedicated state/work paths, verified runner sandbox, whole-process-group shutdown/reaping, startup health and failure reporting. Match returned home/platform to the intended runner. Never pass SQLite/backups, TYPESAFE_API_KEY, API keys or admin credentials into that runner.
2. **Account service (T39):** owner authentication/CSRF, serialized official login attempts, URL validation, cancellation/completion correlation, account/model/limit validation and redacted public DTOs. Missing quota remains unavailable. No API-key fallback.
3. **Run service (T40):** durable dispatch intent/idempotency, one active turn/bounded queue, supported authenticated history reconciliation, event persistence/replay cursors, pending-request authorization and run-scoped MCP identity. Do not interpret transport acknowledgement as a completed turn. These services are not faked here.
4. **Execution gate:** tools stay unavailable until selected-host isolation, required production MCP behavior, owner login, supported history and live turn interruption/crash recovery are verified. Offline client tests do not satisfy these gates.

## Verification

### Bounded intake controller

`NewIntakeController(client, trustedInstructions)` supplies one-active-intake
`Run(ctx, preparedText, hooks)` for collector and URL/paste submissions. It owns
the client's event stream during Run; do not attach a competing event consumer.
The service must provide a single controller and retain/renew its ingestion job
claim. Loss of that claim cancels Run. This package does not claim jobs, start a
runtime, fetch URLs, configure MCP, expose HTTP routes or enable tools.

`IntakeHooks` are closures over the current durable job claim:

- `Ready` verifies the approved isolated runtime and required scoped tools.
  Run additionally requires a ChatGPT account returned by `account/read`.
- `RecordDispatch` first receives empty IDs to record intent before creation;
  subsequent calls bind the returned thread and turn IDs. Fence every update
  to the active claim, reject duplicate dispatch and retain uncertain outcomes
  for an explicit decision. Do not reset unknown dispatch to pending blindly.
- `ReadSaved` returns source-backed persisted opportunity IDs belonging to this
  ingestion, verified against its exact source snapshot. Model prose and item
  output are never a result authority.
- `Finish` records outcome and preserves partial results. Map into the actual
  ingestion schema; preserve an existing `needs_text` result from retrieval.
  The callbacks get a bounded uncancelled context for final recording, but must
  still reject an expired job claim.

Foundation's current adapter mapping is direct: empty `RecordDispatch` calls
`BeginIngestionDispatch`; thread-only calls `BindIngestionThread`; both IDs call
`BindIngestionTurn`. `ReadSaved` reads the result mapping recorded by
`RecordIngestionResult`, which validates the immutable opportunity change against
the ingestion's exact source. `Finish` settles the claimed job through the
application service; do not replace that result mapping with model-returned IDs.
Inspect `IntakeOutcome.State`: Run can return a nil error with `failed`,
`interrupted` or `needs_attention`. Nil error alone never means ingestion succeeded.

Progress is drained while start RPCs are pending, so an early completion is
correlated after the start response. Only the matching thread/turn can finish
the run. A completed turn without a saved opportunity fails with
`no_saved_opportunity`; unavailable result readback remains uncertain. Failed or
interrupted turns preserve trusted partial record links. Cancellation or a lost
acknowledgement attempts interrupt when possible and closes the connection;
it does not assert the remote turn stopped. The controller never retries a turn.

A genuine runtime approval/question becomes `needs_attention`; this minimal
slice attempts interrupt and closes the connection without answering it. It
does not retain an actionable approval session or implement an interactive
approval UI; that capability remains incomplete. Raw request text, error payloads
and assistant prose are not persisted or displayed by this controller. Normal
authorized bridge writes should not require such a question.

This slice is unmounted. Trusted runner configuration, scoped production bridge,
durable-store adapter, owner account service and a real signed-in ingestion turn
remain integration work. Synthetic tests are not live readiness evidence.

Run from `apps/api`, with writable caches:

```sh
GOCACHE=/private/tmp/jobseek-go-cache GOMODCACHE=/private/tmp/jobseek-go-mod go test -race -count=1 ./internal/codex
GOCACHE=/private/tmp/jobseek-go-cache GOMODCACHE=/private/tmp/jobseek-go-mod go vet ./internal/codex
```

Tests use `net.Pipe` and synthetic frames only. They cover handshake/version rejection, out-of-order and unknown/duplicate IDs, numeric/string ID distinctions, timeout/cancellation, blocked writer cleanup, pending/event/write capacity, malformed/oversized/truncated frames, EOF failure, approval generation/reuse/expiry/queued resolution, unsupported requests, typed wire shapes and safe error text. They do not start Codex, log into an account, call a provider or contact a remote host.
