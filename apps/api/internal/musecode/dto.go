package musecode

// PublicCriteria carries only general, non-identifying role/region/skill
// search criteria into a Contributor session. Saved private owner
// requirements stay server-side and bind locally/through Jev.
type PublicCriteria struct {
	RoleKeywords  []string `json:"role_keywords"`
	RegionText    string   `json:"region_text"`
	SkillKeywords []string `json:"skill_keywords"`
}

// PublicVacancy is the complete public projection of one captured vacancy.
// Refs are app-side opaque handles; raw receipts stay server-side.
type PublicVacancy struct {
	VacancyRef   string `json:"vacancy_ref"`
	SourceHost   string `json:"source_host"`
	PageURL      string `json:"page_url"`
	EmployerName string `json:"employer_name"`
	Title        string `json:"title"`
	LocationText string `json:"location_text"`
	PostedText   string `json:"posted_text"`
	// WorkPattern is the evidence-grounded arrangement stated on the
	// listing page: unknown, onsite, hybrid or remote. Anything else
	// the turn reports sanitizes to unknown; missing stays unknown.
	WorkPattern string `json:"work_pattern"`
	CapturedAt  string `json:"captured_at"`
	ReceiptRef  string `json:"receipt_ref"`
}

// SanitizeWorkPattern keeps only the arrangement values the listing
// page may state; every other claim becomes unknown rather than
// inventing a pattern or failing the save.
func SanitizeWorkPattern(pattern string) string {
	switch pattern {
	case "unknown", "onsite", "hybrid", "remote":
		return pattern
	default:
		return "unknown"
	}
}

// PublicQuestion is one actual employer question captured from a public
// vacancy page. Questions are never invented; uncaptured means absent.
type PublicQuestion struct {
	QuestionRef string `json:"question_ref"`
	VacancyRef  string `json:"vacancy_ref"`
	PromptText  string `json:"prompt_text"`
	Required    bool   `json:"required"`
	SourceURL   string `json:"source_url"`
}

// contributorTools is the frozen public-only tool allowlist for
// Contributor sessions. The live CLI is invoked directly (R1): its native
// retrieval tools web_search and web_fetch are the only live names,
// observed as server tool identifiers in the installed 1.4.0 binary. The
// five public_* names are retained only for the stop/resume harness
// transports, which emit them as synthetic events; no live turn can call
// them (no MCP server is served). R2 removes them with the harness. Any
// other addition needs a contract change. Shell, write, browser,
// subagent, memory and private tools are never listed.
var contributorTools = map[string]bool{
	"web_search": true,
	"web_fetch":  true,

	"public_search":        true,
	"public_fetch":         true,
	"public_save_vacancy":  true,
	"public_save_question": true,
	"public_list_saved":    true,
}

// ContributorToolAllowed reports whether a Contributor session may call the
// named tool. Unknown names fail closed.
func ContributorToolAllowed(name string) bool {
	return contributorTools[name]
}

// ContributorToolNames returns the frozen allowlist for audit display.
func ContributorToolNames() []string {
	names := make([]string, 0, len(contributorTools))
	for name := range contributorTools {
		names = append(names, name)
	}
	return names
}
