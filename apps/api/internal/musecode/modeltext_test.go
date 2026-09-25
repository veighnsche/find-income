package musecode

import (
	"context"
	"strings"
	"testing"
)

func TestModelTextCountsTowardByteBounds(t *testing.T) {
	bounds := DefaultBounds()
	bounds.MaxBytesPerOp = 64
	bounds.MaxBytesTotal = 128
	transport := &scriptTransport{events: []Event{
		{Kind: EventModelText, Text: "short text", BytesOut: 10},
		{Kind: EventFinished},
	}}
	supervisor := NewSupervisor(transport, &memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	facts := readyFacts()
	spec, err := NewSession(Check(TierStandard, facts), "/tmp/e13-text-bytes", bounds, []string{"/tmp/e13-text-peer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.StartRun(context.Background(), "text-ok", spec, StandardInput{}, facts); err != nil {
		t.Fatal(err)
	}
	result, ok := supervisor.Result("text-ok")
	if !ok || result.Outcome != OutcomeCompleted || result.Usage.BytesOut != 10 {
		t.Fatalf("result = %+v ok=%v, want completed with 10 bytes out", result, ok)
	}

	oversize := &scriptTransport{events: []Event{
		{Kind: EventModelText, Text: strings.Repeat("x", 65), BytesOut: 65},
		{Kind: EventFinished},
	}}
	supervisor = NewSupervisor(oversize, &memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	if _, err := supervisor.StartRun(context.Background(), "text-over", spec, StandardInput{}, facts); err != nil {
		t.Fatal(err)
	}
	result, ok = supervisor.Result("text-over")
	if !ok || result.Outcome != OutcomeFailed || !strings.Contains(result.Detail, "byte bound") {
		t.Fatalf("result = %+v ok=%v, want failed on the byte bound", result, ok)
	}
}
