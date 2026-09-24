// Package researchmemory implements T11 research memory and immutable
// provenance: transactional exact-request claims with lease recovery,
// freshness/justified refresh, observations, investigation notes, private
// immutable artifacts and server-side receipt resolution.
//
// Owner: lane C. Depends on the T07 store foundation (schema FROZEN) and the
// T06 researchcontract. Lane B registers the handlers (see handlers.go) at
// T17; lane C's executor (T16) calls Memory.Observe for T-observe.
package researchmemory
