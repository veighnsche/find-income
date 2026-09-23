//go:build (!darwin && !linux) || (!amd64 && !arm64)

package codexrunner

import "context"

type process struct{}

func start(context.Context, Config) (*Process, error) { return nil, ErrUnsupported }
func (*process) read([]byte) (int, error)             { return 0, ErrUnsupported }
func (*process) write([]byte) (int, error)            { return 0, ErrUnsupported }
func (*process) close() error                         { return ErrUnsupported }
func (*process) doneChan() <-chan struct{}            { return nil }
func (*process) err() error                           { return ErrUnsupported }
