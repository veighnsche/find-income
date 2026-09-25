// Package publicresearch isolates adaptive public-only vacancy discovery
// (lane R, task E03) behind the five frozen Contributor MCP tools.
//
// The server owns a fresh MCP tool registry serving exactly the frozen
// allowlist (see musecode.ContributorToolNames): inherited host tools,
// plugins and the legacy private context_read tool are never registered.
// Every return is a musecode public DTO or a public-only envelope, so
// private owner, Jev and session fields are dropped by construction.
//
// Sources stay unrestricted: search and fetch accept arbitrary public
// queries, URLs, backends and parameters. No fixed source menu exists.
// Request and byte accounting derives from musecode.Bounds and surfaces
// as budget_exhausted outcomes; invalid input consumes zero allowance.
//
// Execution and evidence reuse call into the shared research contract
// (researchcontract.Executor and researchcontract.CaptureReader), which
// E06 wires to the production researchexecute and researchmemory
// backends with run-scoped authority. This package opens no network
// listener; serving transport and authentication stay with M.
package publicresearch
