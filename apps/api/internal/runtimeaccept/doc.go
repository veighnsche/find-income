// Package runtimeaccept holds lane B's T24 integrated runtime acceptance:
// failure and concurrency behavior of the real T23 wired stack.
//
// Test-only: no production code lives here. Every test wires the full
// research stack (apps/api/internal/researchwire) on an isolated TempDir
// store with loopback fixture servers and a scripted zero-spend Jev
// provider, then exercises two real research operations under one
// allowance, Stop at each dispatch boundary, late receipts and fenced
// writes, changed authority, crash reconnect, context reconstruction,
// retries, unknown spend, browser/script egress mediation and inert
// startup/status reads.
//
// Target-host assertions (Linux confinement, real pins, live runner
// transport) are recorded for T29 in the T24 evidence note, not claimed
// here.
package runtimeaccept
