package store

// Run-operation cost ledger. Owner: lane B (runtime). The mutation dispatch
// and transaction body stay in D-owned round_mutations.go; only the operation
// names and their allowance charges live here so new reviewed operations can
// be added without touching the dispatch path.

const (
	RoundCreateCompany                  = "company.create"
	RoundCreateOpportunity              = "opportunity.create"
	RoundSaveSourceOpportunity          = "opportunity.source_save"
	RoundCorrectPreferences             = "preferences.correct"
	RoundCorrectEvidence                = "evidence.correct"
	RoundCorrectOpportunity             = "opportunity.owner_correction"
	RoundPrepareApplicationPack         = "application_pack.prepare"
	RoundPrepareOfferComparison         = "offer_comparison.prepare"
	RoundRelationshipCounterpartyCreate = "relationship.counterparty_create"
	RoundRelationshipEventCreate        = "relationship.event_create"
	RoundRelationshipRouteCreate        = "relationship.route_create"
	RoundRelationshipCorrect            = "relationship.correct"
	RoundFetchSource                    = "source.fetch"
	RoundSearchSource                   = "source.search"
	RoundCodexTurn                      = "codex.turn"
	RoundContextTool                    = "round.context"
	RoundJevRequest                     = "jev.request"
	RoundJevAssess                      = "jev_assess"
	RoundDeliverApplication             = "application.delivery"
	RoundInterviewBriefSave             = "interview.brief_save"
	RoundInterviewDebriefSave           = "interview.debrief_save"
	RoundReplyUpdateSave                = "reply.update_save"
	RoundReplyDraftSave                 = "reply.draft_save"
	RoundResearchSearch                 = "research.search"
	RoundResearchFetch                  = "research.fetch"
	RoundResearchBrowse                 = "research.browse"
	RoundResearchAPI                    = "research.api"
	RoundResearchExec                   = "research.exec"
)

// ResearchOperations lists the general-research dispatch operations (T06 §8
// execution kinds). Authority reservations serve these with the fixed scope
// resource ResearchAuthorityResource.
func ResearchOperations() []string {
	return []string{RoundResearchSearch, RoundResearchFetch, RoundResearchBrowse, RoundResearchAPI, RoundResearchExec}
}

// IsResearchOperation reports whether op is a general-research dispatch operation.
func IsResearchOperation(op string) bool {
	switch op {
	case RoundResearchSearch, RoundResearchFetch, RoundResearchBrowse, RoundResearchAPI, RoundResearchExec:
		return true
	}
	return false
}

// The caller chooses an operation, never its charge. Reviewed operations are
// added here without changing the reservation ledger.
func RoundOperationCost(operation string) (RoundAllowance, bool) {
	switch operation {
	case RoundCreateCompany, RoundCreateOpportunity, RoundSaveSourceOpportunity, RoundCorrectPreferences, RoundCorrectEvidence, RoundCorrectOpportunity, RoundRelationshipCounterpartyCreate, RoundRelationshipEventCreate, RoundRelationshipRouteCreate, RoundRelationshipCorrect, RoundInterviewBriefSave, RoundInterviewDebriefSave, RoundReplyUpdateSave, RoundReplyDraftSave:
		return RoundAllowance{Requests: 1, Items: 1, Tools: 1}, true
	case RoundFetchSource, RoundSearchSource:
		return RoundAllowance{Requests: 1, Tools: 1}, true
	case RoundResearchSearch, RoundResearchFetch, RoundResearchBrowse, RoundResearchAPI, RoundResearchExec:
		return RoundAllowance{Requests: 1, Tools: 1}, true
	case RoundCodexTurn:
		return RoundAllowance{Tools: 1, Turns: 1}, true
	case RoundContextTool:
		return RoundAllowance{Tools: 1}, true
	case RoundJevRequest, RoundJevAssess:
		return RoundAllowance{Requests: 1}, true
	case RoundDeliverApplication:
		return RoundAllowance{Requests: 1, Items: 1, Tools: 1}, true
	case RoundPrepareApplicationPack, RoundPrepareOfferComparison:
		return RoundAllowance{Requests: 1, Items: 1, Tools: 1}, true
	default:
		return RoundAllowance{}, false
	}
}
