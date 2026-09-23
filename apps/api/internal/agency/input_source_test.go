package agency

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type ownerSourceTransport func(*http.Request) (*http.Response, error)

func (f ownerSourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLeverOwnerSourceReaderReadsOnlyExactVerifiedPosting(t *testing.T) {
	const posting = `{"id":"post-1","text":"Backend engineer","descriptionPlain":"Build Go services in Brussels.","hostedUrl":"https://jobs.lever.co/example/post-1"}`
	var calls int
	reader := LeverOwnerSourceReader{HTTPClient: &http.Client{Transport: ownerSourceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.lever.co/v0/postings/example/post-1" {
			t.Fatalf("unexpected read URL: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(posting))}, nil
	})}}
	text, err := reader.ReadVacancy(context.Background(), "https://jobs.lever.co/example/post-1")
	if err != nil || text != posting || calls != 1 {
		t.Fatalf("verified posting: calls=%d text=%q err=%v", calls, text, err)
	}
	for _, url := range []string{"http://jobs.lever.co/example/post-1", "https://jobs.lever.co/example/post-1?x=1", "https://other.example/example/post-1", "https://jobs.lever.co/example/post-1/extra"} {
		if _, err := reader.ReadVacancy(context.Background(), url); err != ErrUnsupportedOwnerSource {
			t.Fatalf("unapproved URL %q: %v", url, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unapproved URL made network request: %d", calls)
	}
}
