package rounds

import (
	"context"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// The T13 per-operation placeholder is calibrated in T22 from T02 proof
// measurements (slowest live op 359ms in a 1.6s run; largest live body
// 559 bytes): 60s outer deadline over the executor's 30s internal op
// default, 1 MiB per response body, 32 requests per operation including
// browser subresources.
func TestSupervisorCalibratedDefaults(t *testing.T) {
	h := newSupervisorHarness(t, SupervisorConfig{})
	if h.sup.perOp != 60*time.Second {
		t.Fatalf("default per-op timeout = %v, want 60s", h.sup.perOp)
	}
	out := h.commission(t, "bounds", "bounds-1")
	bounds, err := h.sup.RunBoundsFor(context.Background(), out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if bounds.PerOpTimeout != 60*time.Second || bounds.MaxBodyBytes != int64(1<<20) || bounds.MaxRequests != 32 {
		t.Fatalf("RunBoundsFor = %+v, want 60s/1MiB/32", bounds)
	}
}

func TestClampDispatchBounds(t *testing.T) {
	cases := []struct {
		name  string
		in    researchcontract.Bounds
		perOp time.Duration
		want  researchcontract.Bounds
	}{
		{"zero selects executor defaults", researchcontract.Bounds{}, time.Minute,
			researchcontract.Bounds{}},
		{"under ceiling passes through",
			researchcontract.Bounds{MaxBytes: 4096, MaxRequests: 3, DeadlineMs: 5000}, time.Minute,
			researchcontract.Bounds{MaxBytes: 4096, MaxRequests: 3, DeadlineMs: 5000}},
		{"over ceiling clamps",
			researchcontract.Bounds{MaxBytes: 1 << 40, MaxRequests: 10000, DeadlineMs: 3600000}, time.Minute,
			researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 32, DeadlineMs: 60000}},
		{"deadline follows configured per-op",
			researchcontract.Bounds{DeadlineMs: 3600000}, 90 * time.Second,
			researchcontract.Bounds{DeadlineMs: 90000}},
		{"negatives pass through for executor rejection",
			researchcontract.Bounds{MaxBytes: -1, MaxRequests: -2, DeadlineMs: -3}, time.Minute,
			researchcontract.Bounds{MaxBytes: -1, MaxRequests: -2, DeadlineMs: -3}},
	}
	for _, tc := range cases {
		if got := clampDispatchBounds(tc.in, tc.perOp); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Over-ceiling caller bounds reach the executor clamped, so a sloppy or
// hostile research_execute call cannot widen one operation past the
// calibrated per-request/per-operation limits.
func TestSupervisorDispatchClampsBounds(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "clamp", "clamp-1")

	_, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request:        testDescriptor("https://example.com/roles"),
		IdempotencyKey: "op-clamp",
		Bounds:         researchcontract.Bounds{MaxBytes: 1 << 40, MaxRequests: 10000, DeadlineMs: 3600000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.exec.callCount() != 1 {
		t.Fatalf("executor calls = %d, want 1", h.exec.callCount())
	}
	got := h.exec.calls[0].Bounds
	want := researchcontract.Bounds{MaxBytes: 1 << 20, MaxRequests: 32, DeadlineMs: 60000}
	if got != want {
		t.Fatalf("executor bounds = %+v, want %+v", got, want)
	}
}
