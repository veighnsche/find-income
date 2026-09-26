// Package publicresearch isolates adaptive public-only vacancy discovery
// behind deterministic evidence primitives plus a retained harness-only
// MCP registry.
//
// Production path (R1, direct.go): the Contributor CLI runs with its own
// native web tools and returns structured sightings with source URLs; the
// app fetches every cited URL itself (FetchURL) and saves only what its
// own captures verify (SaveVacancy/SaveQuestion). The model never calls
// this server.
//
// The server also owns a frozen five-tool MCP registry serving exactly
// the legacy allowlist (see musecode.ContributorToolNames): inherited
// host tools, plugins and the legacy private context_read tool are never
// registered. Only the stop/resume harness drives saves through it; R2
// removes it with the harness.
//
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
