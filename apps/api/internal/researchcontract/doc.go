// Package researchcontract publishes the T06 shared implementation contract for
// the autonomous recruitment replacement: settled outcome codes, the
// backend-issued execution receipt and capture shapes, exact-request
// fingerprint rules, and the authority, memory, executor, Jev, identity and
// record-save interfaces every lane compiles and tests against.
//
// Owner: lane A (supervisor). Other lanes implement these interfaces in their
// own files; they never edit this package. Contract changes land here first
// in one coordinated patch. Stdlib only: importing this package cannot create
// an import cycle.
package researchcontract
