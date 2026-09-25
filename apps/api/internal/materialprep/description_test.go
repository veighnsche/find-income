package materialprep

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type fakeCaptures struct {
	bodies map[string]string
	err    error
	opened []string
}

func (f *fakeCaptures) ResolveReceipt(_ context.Context, _ string) (researchcontract.ExecutionReceipt, error) {
	return researchcontract.ExecutionReceipt{}, errors.New("no receipts in fixture")
}

func (f *fakeCaptures) OpenCapture(_ context.Context, captureID string) (researchcontract.Capture, io.ReadCloser, error) {
	f.opened = append(f.opened, captureID)
	if f.err != nil {
		return researchcontract.Capture{}, nil, f.err
	}
	return researchcontract.Capture{ID: captureID},
		io.NopCloser(strings.NewReader(f.bodies[captureID])), nil
}

func TestRoleDescriptionPrefersSavedText(t *testing.T) {
	captures := &fakeCaptures{bodies: map[string]string{"cap-1": "capture bytes"}}
	svc := &Service{Captures: captures}
	got, err := svc.roleDescription(context.Background(),
		store.Opportunity{OriginalText: "saved text"}, []string{"cap-1"})
	if err != nil || got != "saved text" {
		t.Fatalf("description = %q %v, want saved text", got, err)
	}
	if len(captures.opened) != 0 {
		t.Fatalf("capture opened despite saved text: %v", captures.opened)
	}
}

func TestRoleDescriptionFallsBackToVacancyCapture(t *testing.T) {
	captures := &fakeCaptures{bodies: map[string]string{"cap-1": "vacancy markdown"}}
	svc := &Service{Captures: captures}
	got, err := svc.roleDescription(context.Background(),
		store.Opportunity{}, []string{"cap-1", "cap-2"})
	if err != nil || got != "vacancy markdown" {
		t.Fatalf("description = %q %v", got, err)
	}
	if len(captures.opened) != 1 || captures.opened[0] != "cap-1" {
		t.Fatalf("opened = %v, want first capture only", captures.opened)
	}
}

func TestRoleDescriptionHonestWhenUndescribed(t *testing.T) {
	svc := &Service{Captures: &fakeCaptures{}}
	got, err := svc.roleDescription(context.Background(), store.Opportunity{}, nil)
	if err != nil || got != "" {
		t.Fatalf("no captures = %q %v, want empty", got, err)
	}
	blank := &Service{Captures: &fakeCaptures{bodies: map[string]string{"cap-1": "  \n "}}}
	got, err = blank.roleDescription(context.Background(), store.Opportunity{}, []string{"cap-1"})
	if err != nil || got != "" {
		t.Fatalf("blank capture = %q %v, want empty", got, err)
	}
	nilReader := &Service{}
	got, err = nilReader.roleDescription(context.Background(), store.Opportunity{}, []string{"cap-1"})
	if err != nil || got != "" {
		t.Fatalf("nil reader = %q %v, want empty", got, err)
	}
}

func TestRoleDescriptionFailsClosed(t *testing.T) {
	oversize := &Service{Captures: &fakeCaptures{
		bodies: map[string]string{"cap-1": strings.Repeat("x", maxRoleDescriptionBytes+1)}}}
	if _, err := oversize.roleDescription(context.Background(), store.Opportunity{}, []string{"cap-1"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("oversize capture: %v, want ErrInvalid", err)
	}
	boom := errors.New("synthetic capture failure")
	unreadable := &Service{Captures: &fakeCaptures{err: boom}}
	if _, err := unreadable.roleDescription(context.Background(), store.Opportunity{}, []string{"cap-1"}); !errors.Is(err, boom) {
		t.Fatalf("unreadable capture: %v, want the capture error", err)
	}
}
