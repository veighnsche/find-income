package codex

import (
	"errors"
	"fmt"
)

var (
	ErrUnavailable        = errors.New("codex runtime unavailable")
	ErrClosed             = errors.New("codex connection closed")
	ErrNotInitialized     = errors.New("codex connection not initialized")
	ErrAlreadyInitialized = errors.New("codex initialization already attempted")
	ErrIncompatible       = errors.New("codex runtime version incompatible")
	ErrMalformedFrame     = errors.New("codex malformed protocol frame")
	ErrFrameTooLarge      = errors.New("codex protocol frame too large")
	ErrBackpressure       = errors.New("codex protocol capacity exceeded")
	ErrUnsupported        = errors.New("codex operation unsupported")
	ErrStaleRequest       = errors.New("codex server request no longer pending")
	ErrInvalidArgument    = errors.New("codex invalid argument")
	ErrHistoryIncomplete  = errors.New("codex dispatch history incomplete")
)

// RPCError deliberately discards upstream message/data. They can contain prompts,
// credentials or filesystem paths and must not become HTTP errors or log text.
type RPCError struct{ Code int }

func (e *RPCError) Error() string        { return fmt.Sprintf("codex RPC error (%d)", e.Code) }
func (e *RPCError) Is(target error) bool { return target == ErrUnsupported && e.Code == -32601 }
