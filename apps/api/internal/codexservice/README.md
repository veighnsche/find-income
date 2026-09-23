# Codex round runtime

This package owns the private Codex App Server connection, official ChatGPT device sign-in, exact-turn lifecycle, and the application-hosted scoped MCP bridge. It never starts recruitment from Status, sign-in, reconnect, page load or a queued job. `ExecuteRoundTurn` runs only when the commissioned round work loop explicitly calls it. The selected host, owner login, authenticated production MCP calls and signed-in history reads still need I12 validation.

## Private runner configuration

The application reads `JOBSEEK_CODEX_SSH_HOST`, `JOBSEEK_CODEX_SSH_USER`, `JOBSEEK_CODEX_SSH_IDENTITY_FILE`, `JOBSEEK_CODEX_SSH_KNOWN_HOSTS`, `JOBSEEK_CODEX_REMOTE_LAUNCHER`, `JOBSEEK_CODEX_ISOLATION_VERIFIED`, `JOBSEEK_CODEX_BRIDGE_TOKEN`, `JOBSEEK_CODEX_MODEL` and `JOBSEEK_CODEX_EFFORT`. Missing or invalid configuration fails closed. The isolation flag is an operator assertion after real containment checks; SSH by itself is not isolation. No local Codex or paid API-key fallback exists.

Install `cmd/codex-runner` on a separately verified host behind a fixed restricted SSH launcher. Its trusted environment supplies `JOBSEEK_RUNNER_CODEX_BINARY`, `JOBSEEK_RUNNER_CODEX_SHA256`, `JOBSEEK_RUNNER_STATE_DIR` and `JOBSEEK_RUNNER_WORK_DIR`. Keep application SQLite, backups, Jev/API/admin credentials and unrelated user files off the runner. The launcher must not inherit the dashboard environment or existing desktop Codex state. The pinned supervisor controls its process group but is not an outer security boundary.

The runner's dedicated Codex configuration uses ChatGPT login and a required `jobseek` MCP server at the private application endpoint. Replace placeholders only in the isolated runner configuration:

```toml
forced_login_method = "chatgpt"
cli_auth_credentials_store = "file"
approvals_reviewer = "user"

[mcp_servers.jobseek]
url = "https://PRIVATE-DASHBOARD/api/v1/codex/mcp"
required = true
enabled_tools = ["round_context", "round_mutation", "round_evidence_correction", "source_links"]

[mcp_servers.jobseek.http_headers]
Authorization = "Bearer DEDICATED-BRIDGE-SECRET"
```

The [official Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp) documents required HTTP servers and headers. This exact private configuration has not been tested on the selected runner.

## Authority and bounded work

The MCP endpoint requires its dedicated bearer credential and rejects browser cookies and Origin headers. That transport credential grants no record authority. Every tool call also carries a random per-turn capability stored only as a digest and bound to one dispatched round attempt, delegated actor and generation. Stop, Resume, expiry and revocation fence older capabilities. `round_context`, `round_mutation`, and the owner-selected `round_evidence_correction` spend server-owned operation costs in the round store; `source_links` spends one request and one tool per page. A repeated request key cannot repeat an external fetch or mutation.

`source_links` accepts a scoped existing `company:<id>` resource and reads that company's stored website server-side. The model cannot supply a URL. It makes one HTTPS GET to a public DNS address pinned to the TLS connection, without redirects, under one MiB, ten seconds and the saved round deadline. Each response returns at most 64 unranked URL/text anchors, a content SHA-256, next offset and omission counts. A continuation uses a new charged request key and the previous content digest. These are link candidates, not verified employers or vacancies. The source-candidate/origin path beyond known companies belongs to I08.

`ExecuteRoundTurn` persists dispatch intent before remote calls, binds returned thread and turn IDs, and treats missing acknowledgements, owner-attention requests and disconnects as uncertain. It never turns model prose into a saved result. Stop cancels locally owned turns and source fetches after the durable generation fence; late callbacks are evidence only. Exact authenticated history observation is required before an uncertain Codex turn can be reconciled. `CheckRound` checks account, selected model/effort, quota and required tools for `discover` without launching work. `Status.IngestionAvailable` remains false until the commissioned work loop and live gates are bound.

Fixture tests cover protocol correlation, failed ID writes, charged MCP calls, cancellation, scope and public-address rejection. A separate public Shopify homepage probe found its careers link on the second SHA-bound page; it did not use a Codex account or runner and did not establish vacancy evidence. Full live host/account/MCP/history and containment acceptance remains open.
