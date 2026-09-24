// Package researchexecute implements researchcontract.Executor: source-independent
// general research execution (search/fetch/browse/api/exec) with automatic
// reservation/reuse and authentic capture.
//
// Owner: lane C (research/storage), T16. It productionizes the T02 proof
// binding (isolated Playwright headless shell + subprocess driver, real
// helpers in rounds/ + store/) on top of the T11 memory
// (researchmemory.Memory/Captures) and the T12 run authority behind the T06
// authority interface. Real supervisor integration is T23; T17 (lane B) wires
// the tool registry.
//
// Request-descriptor conventions (the contract shape is frozen; this is how
// the executor interprets it per kind):
//
//   - Backend names the dispatch backend and is validated strictly: search,
//     fetch and api require "generic-http"; browse requires
//     "chromium-headless-shell"; exec requires "python3-sandbox".
//   - Method: search/fetch/browse accept "" (GET) or GET; api accepts "" (GET),
//     GET or POST; exec requires "". Any other verb is rejected pre-dispatch
//     with zero allowance consumed.
//   - URLOrQuery: the base URL (search), URL (fetch/browse) or endpoint (api);
//     exec requires exactly "python3" (the only runtime) with the program in
//     Body. Bodies are rejected on every kind except api POST and exec.
//   - Params become the query string in order (repeats preserved), followed by
//     non-empty Pagination fields as page/cursor/limit. A param named
//     Content-Type (case-insensitive) on api POST is sent as the request
//     header instead of the query; anywhere else it is rejected.
//   - SessionFields honors exactly two result-affecting fields: locale
//     (Accept-Language for HTTP kinds, --lang for browse) and viewport
//     (WxH, browse --window-size only). Unknown or empty fields are rejected:
//     silently ignoring a potentially result-affecting field would break
//     exact-request semantics.
//
// Egress mediation (closes the T02 §8 gaps): browser and exec children run
// under a per-operation seatbelt profile that permits outbound IP only to the
// operation's own recording proxy port; the proxy observes every request and
// enforces the destination policy. There is no unobserved bypass: when the
// sandbox backend is unavailable the affected kinds fail closed with an
// explicit error code instead of running unmediated.
package researchexecute
