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
	CapturedAt   string `json:"captured_at"`
	ReceiptRef   string `json:"receipt_ref"`
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

// contributorTools is the frozen initial public-only MCP tool allowlist for
// Contributor sessions. E03 implements these tools; any addition needs an M
// contract change. The legacy private context_read tool is never listed.
var contributorTools = map[string]bool{
	"public_search":        true,
	"public_fetch":         true,
	"public_save_vacancy":  true,
	"public_save_question": true,
	"public_list_saved":    true,
}

// ContributorToolAllowed reports whether a Contributor session may call the
// named MCP tool. Unknown names fail closed.
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
