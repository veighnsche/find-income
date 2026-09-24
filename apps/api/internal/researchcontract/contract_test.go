package researchcontract

import (
	"strings"
	"testing"
)

func baseDescriptor() RequestDescriptor {
	return RequestDescriptor{
		Operation:  OperationFetch,
		Backend:    "http-direct",
		Method:     "GET",
		URLOrQuery: "https://example.com/jobs?page=2",
		Params:     []Param{{Name: "page", Value: "2"}},
		Pagination: Pagination{Page: "2"},
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	a, err := baseDescriptor().Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	b, err := baseDescriptor().Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(a) != 64 {
		t.Fatalf("fingerprint not deterministic: %q %q", a, b)
	}
}

func TestFingerprintSignificantDifferences(t *testing.T) {
	base, err := baseDescriptor().Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*RequestDescriptor){
		"operation": func(d *RequestDescriptor) { d.Operation = OperationSearch },
		"backend":   func(d *RequestDescriptor) { d.Backend = "other" },
		"method":    func(d *RequestDescriptor) { d.Method = "POST" },
		"url":       func(d *RequestDescriptor) { d.URLOrQuery = "https://example.com/jobs?page=3" },
		"param-add": func(d *RequestDescriptor) { d.Params = append(d.Params, Param{Name: "x", Value: "1"}) },
		"param-repeat": func(d *RequestDescriptor) {
			d.Params = []Param{{Name: "page", Value: "2"}, {Name: "page", Value: "2"}}
		},
		"param-order": func(d *RequestDescriptor) {
			d.Params = []Param{{Name: "b", Value: "1"}, {Name: "a", Value: "1"}}
		},
		"pagination": func(d *RequestDescriptor) { d.Pagination = Pagination{Page: "3"} },
		"body":       func(d *RequestDescriptor) { d.BodySHA256 = strings.Repeat("a", 64) },
	}
	for name, mutate := range cases {
		d := baseDescriptor()
		if name == "param-order" {
			d.Params = []Param{{Name: "a", Value: "1"}, {Name: "b", Value: "1"}}
			ordered, err := d.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			mutate(&d)
			changed, err := d.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if ordered == changed {
				t.Fatalf("%s: order change did not alter fingerprint", name)
			}
			continue
		}
		mutate(&d)
		got, err := d.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatalf("%s: significant change did not alter fingerprint", name)
		}
	}
}

func TestFingerprintAllowlistedNormalization(t *testing.T) {
	a := baseDescriptor()
	b := baseDescriptor()
	b.URLOrQuery = "HTTPS://EXAMPLE.COM/jobs?page=2"
	fa, err := a.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fb, err := b.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fa != fb {
		t.Fatal("scheme/host case folding must be insignificant")
	}
}

func TestFingerprintRejectsSecrets(t *testing.T) {
	for _, name := range []string{"api_key", "Token", "PASSWORD", "secret", "auth"} {
		d := baseDescriptor()
		d.Params = []Param{{Name: name, Value: "x"}}
		if _, err := d.Fingerprint(); err == nil {
			t.Fatalf("param %q accepted", name)
		} else if cerr, ok := err.(*Error); !ok || cerr.Code != OutcomeInvalid {
			t.Fatalf("param %q: wrong error type %v", name, err)
		}
	}
}

func TestFingerprintRejectsShape(t *testing.T) {
	d := baseDescriptor()
	d.Operation = "crawl"
	if _, err := d.Fingerprint(); err == nil {
		t.Fatal("unknown operation accepted")
	}
	d = baseDescriptor()
	d.Backend = ""
	if _, err := d.Fingerprint(); err == nil {
		t.Fatal("empty backend accepted")
	}
}

func TestOutcomeValid(t *testing.T) {
	for _, o := range []Outcome{OutcomeOK, OutcomeReused, OutcomeClaimedElsewhere, OutcomeStale, OutcomeRevisionConflict, OutcomeIdentityAmbiguous, OutcomeCaptureIncomplete, OutcomeBudgetExhausted, OutcomeStopped, OutcomeRateLimited, OutcomeUncertain, OutcomeInvalid, OutcomeConflict, OutcomeForbidden, OutcomeNotFound} {
		if !o.Valid() {
			t.Fatalf("outcome %q invalid", o)
		}
	}
	if Outcome("bogus").Valid() {
		t.Fatal("bogus outcome valid")
	}
}

func TestReceiptStatusOutcomeMapping(t *testing.T) {
	cases := map[ReceiptStatus]Outcome{
		ReceiptOK: ReceiptOK.Outcome(), ReceiptLate: OutcomeOK, ReceiptShared: OutcomeOK,
		ReceiptReused: OutcomeReused, ReceiptFailed: OutcomeInvalid,
		ReceiptCanceled: OutcomeStopped, ReceiptUncertain: OutcomeUncertain,
	}
	for status, want := range cases {
		if !status.Valid() {
			t.Fatalf("status %q invalid", status)
		}
		if got := status.Outcome(); got != want {
			t.Fatalf("status %q maps to %q, want %q", status, got, want)
		}
	}
	if ReceiptStatus("bogus").Valid() {
		t.Fatal("bogus receipt status valid")
	}
}

func TestContractErrorMessage(t *testing.T) {
	err := NewError(OutcomeForbidden, string(AuthorityPermission), "record.write denied for this run")
	if !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("unexpected message %q", err.Error())
	}
}
