// Package researchwire composes the autonomous recruitment backend (T23):
// the real executor, capture/memory store, Jev assessor, identity matcher,
// record saver and run supervisor, plus the MCP toolchain and the HTTP
// adapter. Owner: lane A (supervisor). It constructs lane implementations
// through their public constructors only; it never edits their behavior.
package researchwire
