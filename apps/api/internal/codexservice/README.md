# Codex ingestion service

This service wires the current ingestion queue to the bounded Codex controller
and official MCP Go SDK. Collector and owner URL/text requests share one path.
Jev organisation is scheduled by the store after the sourced opportunity save;
it is not a prerequisite for ingestion.

## Runtime configuration

The application reads only these dedicated settings:

- `JOBSEEK_CODEX_SSH_HOST`, `JOBSEEK_CODEX_SSH_USER`
- `JOBSEEK_CODEX_SSH_IDENTITY_FILE`, `JOBSEEK_CODEX_SSH_KNOWN_HOSTS` (absolute paths)
- `JOBSEEK_CODEX_REMOTE_LAUNCHER` (fixed absolute remote executable path)
- `JOBSEEK_CODEX_ISOLATION_VERIFIED=true` after actual host verification
- `JOBSEEK_CODEX_BRIDGE_TOKEN` (dedicated random secret, at least 32 characters)

Absent configuration returns `runner_not_configured`; absent verification returns
`isolation_not_verified`. SSH authenticates the configured host but does not prove
filesystem isolation. No host is hardcoded and no local Codex fallback exists.
The service leaves ingestion jobs queued until account and required tools are
available. Configuration alone is not evidence of a successful live ingestion.

Install `cmd/codex-runner` only on the separately verified isolated host, behind
the restricted SSH launcher. Its trusted launcher environment supplies
`JOBSEEK_RUNNER_CODEX_BINARY`, `JOBSEEK_RUNNER_CODEX_SHA256`,
`JOBSEEK_RUNNER_STATE_DIR` and `JOBSEEK_RUNNER_WORK_DIR`. It uses the existing
pinned supervisor, dedicated 0700 directories and minimal child environment.
The launcher must not inherit/mount application databases, backups or provider
keys. Keep binary/config paths immutable to agent writes. Provisioning and
target-host isolation verification are not performed by this package.

The runner's dedicated Codex configuration uses forced ChatGPT login and a
required `jobseek` MCP server pointing to the application's private HTTPS
`/api/v1/codex/mcp` endpoint. Set its Authorization header to the dedicated bridge
token through trusted runner configuration. Do not copy the user's desktop
configuration/authentication. Example shape (replace placeholders in the
isolated runner only):

```toml
forced_login_method = "chatgpt"
cli_auth_credentials_store = "file"
approvals_reviewer = "user"

[mcp_servers.jobseek]
url = "https://PRIVATE-DASHBOARD/api/v1/codex/mcp"
required = true
enabled_tools = ["ingestion_context", "fetch_vacancy", "save_vacancy", "source_context", "add_evidence"]

[mcp_servers.jobseek.http_headers]
Authorization = "Bearer DEDICATED-BRIDGE-SECRET"
```

The [official Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp)
documents HTTP URLs, headers and required servers. This exact private connection
still needs validation on the configured pinned runtime; no live login/tool test
has been claimed. No application or provider secret is forwarded in the SSH
process environment.

## Routes and tools

Owner-authenticated `GET /api/v1/codex/status` distinguishes unavailable,
needs_sign_in, connecting and ready. Connect/cancel are CSRF-protected POSTs.
Connect returns only an official HTTPS device verification URL and user code;
the owner completes sign-in. Pending attempts use a documented local ten-minute
timeout, not a claimed provider expiry. Status checks the runtime account and
required discovered tools; missing account or tool readiness never enables claims.

The MCP endpoint uses dedicated bearer authentication, rejects browser cookies
and Origin headers, and shares one tool implementation. Each call also requires
an opaque random capability mapped in memory to exactly one active leased intake.
The capability is revoked after the run; callers cannot supply an actor, choose
another intake or write an unrelated opportunity. Late calls from an earlier run
cannot use the next run's identity. Tool errors omit raw database/network details.

`save_vacancy` invokes the atomic store operation, forcing exact source and
discovered stage and returning the same result on repeat. `source_context` returns
the actual saved source and current evidence versions. `add_evidence` requires a
unique exact quotation and uses the store's lease-fenced ingestion writer. Its
source/actor/opportunity come from the assigned intake; it cannot create owner
workability or confirmed actual salary. Published partial pay remains representable.
The existing evidence version rejects a repeated stale write; it is not blindly
retried. The bridge currently represents alternative terms as unresolved rather
than creating option sets; richer extraction can use the existing domain API later.

A saved core record is a partial result until its Codex turn finishes successfully.
Failed processing stays failed with the saved record linked; explicit retry runs
another turn against that same mapping. A separate intake of the same exact source
links the existing current record; differing source text is not silently merged.

URL retrieval uses only the assigned public HTTP(S) URL, checks resolved addresses
and each redirect, disables proxies and bounds size/time. Retrieved UTF-8 text or
HTML is preserved verbatim. Inaccessible/non-text/oversized pages become
`needs_text`; no guessed vacancy replaces the source. It is a single-page fetch,
not a general crawler or authenticated browser.

## Integration and limits

Startup constructs `NewFromEnvironment(ctx, db)`, injects the service into HTTP
options, runs `Service.Run` separately from the organisation worker and joins it
before closing the store. No request handler starts an ingestion turn. Existing
job heartbeats retain the lease; losing it cancels the controller. A runtime turn
is bounded to ten minutes. Unknown dispatched outcomes require explicit retry
review; they are never automatically replayed. The adapter checks outcome state,
not merely nil error. Genuine runtime questions currently interrupt/close and
return a needs-attention failure; there is no actionable approval UI yet.

Synthetic tests exercise an actual official SDK HTTP client/server, a simulated
App Server transport, fresh SQLite persistence, source-backed partial pay,
repeat-save deduplication and capability revocation. They do not establish SSH
host readiness, sandbox isolation, owner login or a real model turn. Those remain
explicit live prerequisites. No model selector or full conversation UI is added.
