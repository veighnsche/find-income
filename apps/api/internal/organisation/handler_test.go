package organisation

import (
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
	"strings"
	"testing"
)

func TestLongSourceUsesBoundedExcerptsAcrossWholeText(t *testing.T) {
	text := "Beginning: backend role. " + strings.Repeat("é", 25000) + " Ending: platform role."
	facts, err := sourcedFacts(store.OrganisationSnapshot{OriginalText: text, SourceID: "source-1", SourceRevision: "revision-1"})
	if err != nil || len(facts) != maxFacts {
		t.Fatalf("bounded source facts: count=%d err=%v", len(facts), err)
	}
	if !strings.Contains(facts[0].Excerpt, "Beginning:") || !strings.Contains(facts[len(facts)-1].Excerpt, "Ending:") {
		t.Fatalf("source ends missing from excerpts: first=%q last=%q", facts[0].Excerpt, facts[len(facts)-1].Excerpt)
	}
	for _, fact := range facts {
		if len(fact.Excerpt) > maxFactBytes || !strings.Contains(text, fact.Excerpt) {
			t.Fatalf("invalid source excerpt: %+v", fact)
		}
	}
}
