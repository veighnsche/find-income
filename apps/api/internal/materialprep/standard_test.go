package materialprep_test

import (
	"context"

	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

// fakeStandardRunner is the provider-disabled Standard fixture: it records
// inputs and replays scripted model texts. No live model, Jev, network,
// send, or secrets are touched.
type fakeStandardRunner struct {
	calls  int
	inputs []musecode.StandardInput
	fn     func(musecode.StandardInput) ([]string, error)
}

func (f *fakeStandardRunner) RunStandard(_ context.Context, input musecode.StandardInput) (materialprep.StandardResult, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	messages, err := f.fn(input)
	if err != nil {
		return materialprep.StandardResult{}, err
	}
	return materialprep.StandardResult{Messages: messages}, nil
}
