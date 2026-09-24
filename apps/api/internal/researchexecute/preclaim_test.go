package researchexecute

import (
	"context"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Pre-claim refusals (T24 F2/F3 signal): descriptor validation and authority
// check failures wrap in PreClaimRefusal carrying the cause, fence errors
// pass through unwrapped for the stop/reconcile path, and the wrapper is
// transparent to contract-error readers.
func TestPreClaimRefusalSignal(t *testing.T) {
	ctx := context.Background()

	invalidIn := researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteAPI, RunID: "run-1", Generation: 1,
		Request: researchcontract.RequestDescriptor{
			Operation: researchcontract.OperationAPI, Backend: BackendHTTP,
			Method: "PUT", URLOrQuery: "https://example.com/x",
		},
	}
	_, err := newTestEnv(t, Config{}).ex.Execute(ctx, invalidIn)
	cause, ok := RefusedPreClaim(err)
	if !ok {
		t.Fatalf("invalid descriptor not signaled: %v", err)
	}
	var cerr *researchcontract.Error
	if !errors.As(cause, &cerr) || cerr.Code != researchcontract.OutcomeInvalid {
		t.Fatalf("refusal cause: %v", cause)
	}
	if err.Error() != cause.Error() {
		t.Fatalf("wrapper changed the message: %q vs %q", err.Error(), cause.Error())
	}

	budgetEnv := newTestEnv(t, Config{})
	budgetEnv.auth.failCheck = researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
		string(researchcontract.AuthorityAllowance), "fake allowance exhausted")
	in := fetchInput("https://example.com/x")
	in.RunID, in.Generation = "run-1", 1
	_, err = budgetEnv.ex.Execute(ctx, in)
	if cause, ok := RefusedPreClaim(err); !ok {
		t.Fatalf("budget check refusal not signaled: %v", err)
	} else if !errors.As(cause, &cerr) || cerr.Code != researchcontract.OutcomeBudgetExhausted {
		t.Fatalf("refusal cause: %v", cause)
	}

	stoppedEnv := newTestEnv(t, Config{})
	stoppedEnv.auth.failCheck = researchcontract.NewError(researchcontract.OutcomeStopped, "run", "fenced")
	if _, err = stoppedEnv.ex.Execute(ctx, in); err == nil {
		t.Fatal("fenced check succeeded")
	} else if _, ok := RefusedPreClaim(err); ok {
		t.Fatalf("fence error signaled as refusal: %v", err)
	}

	if _, ok := RefusedPreClaim(nil); ok {
		t.Fatal("nil error signaled as refusal")
	}
	if _, ok := RefusedPreClaim(errors.New("plain failure")); ok {
		t.Fatal("plain error signaled as refusal")
	}
}
