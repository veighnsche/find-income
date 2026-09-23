package store

import "testing"

func TestDeliveryRouteCandidateChecksOnlyExactEvidenceAndMailboxIdentity(t *testing.T) {
	opening := "Contact hello@employer.example for information. Apply by email to jobs@employer.example. Apply online at https://ats.example/jobs/1."
	good := OpportunityRoute{OpportunityRouteInput: OpportunityRouteInput{Kind: "direct", SourceKind: "application_instruction",
		DestinationText: "jobs@employer.example", SourceExcerpt: "Apply by email to jobs@employer.example."}}
	if !DeliveryRouteCandidate(good, opening) {
		t.Fatal("exact cited mailbox rejected")
	}
	contact := good
	contact.DestinationText = "hello@employer.example"
	contact.SourceExcerpt = "Contact hello@employer.example for information."
	if !DeliveryRouteCandidate(contact, opening) {
		t.Fatal("semantic contact classification leaked into deterministic candidate check")
	}
	portal := good
	portal.DestinationText = "https://ats.example/jobs/1"
	portal.SourceExcerpt = "Apply online at https://ats.example/jobs/1."
	if DeliveryRouteCandidate(portal, opening) {
		t.Fatal("public ATS GET became submission permission")
	}
	invented := good
	invented.SourceExcerpt = "Send your application to jobs@employer.example."
	if DeliveryRouteCandidate(invented, opening) {
		t.Fatal("uncited instruction became submission route")
	}
}
