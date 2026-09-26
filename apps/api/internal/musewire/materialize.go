package musewire

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
)

// Discovery materialization (R1). The live turn returns structured
// sightings; this file parses them, fetches every cited page URL through
// the run server, and saves only vacancies the app's own captures verify.
// See the evidence/save boundary in publicresearch/direct.go: model
// sightings are claims, app captures are evidence, and anything between
// them that fails becomes an honest gap.

// maxMaterializedVacancies bounds the sightings one turn can materialize.
// The supervisor already bounded the turn itself; this is the parsing
// backstop against a runaway structured answer.
const maxMaterializedVacancies = 200

// discoverySighting is one model-reported vacancy. Fields are claims until
// the app fetches page_url and binds them to the trusted receipt.
type discoverySighting struct {
	PageURL      string `json:"page_url"`
	EmployerName string `json:"employer_name"`
	Title        string `json:"title"`
	LocationText string `json:"location_text"`
	PostedText   string `json:"posted_text"`
}

// discoveryOutput is the parsed discovery turn product.
type discoveryOutput struct {
	Vacancies       []discoverySighting `json:"vacancies"`
	SourcesSearched []string            `json:"sources_searched"`
	Gaps            []string            `json:"gaps"`
}

// parseDiscoveryOutput decodes one turn's final text. The meta provider's
// --output-schema guarantees the shape; a single fenced block is also
// accepted. Anything else fails closed: the caller records the gap and
// saves nothing from that text.
func parseDiscoveryOutput(text string) (discoveryOutput, error) {
	var out discoveryOutput
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return out, fmt.Errorf("musewire: turn returned no structured findings")
	}
	if fenced, ok := unfenceJSON(trimmed); ok {
		trimmed = fenced
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("musewire: turn text is not the discovery object: %w", err)
	}
	if len(out.Vacancies) > maxMaterializedVacancies {
		return out, fmt.Errorf("musewire: turn reported %d vacancies, over the %d-item gate",
			len(out.Vacancies), maxMaterializedVacancies)
	}
	return out, nil
}

// unfenceJSON strips one ```json fences wrapper, if present.
func unfenceJSON(text string) (string, bool) {
	if !strings.HasPrefix(text, "```") {
		return text, false
	}
	inner := strings.TrimPrefix(text, "```")
	if idx := strings.Index(inner, "\n"); idx >= 0 {
		inner = inner[idx+1:]
	} else {
		return text, false
	}
	end := strings.LastIndex(inner, "```")
	if end < 0 {
		return text, false
	}
	return strings.TrimSpace(inner[:end]), true
}

// materializeDiscovery fetches and saves every sighting in the turn's new
// texts. Refs already saved (by an earlier text or a resumed turn's
// re-report) converge on the same vacancy via URL dedupe; per-sighting
// failures become gaps. It returns the newly collected refs in save order
// plus the gaps to disclose.
func materializeDiscovery(ctx context.Context, server *publicresearch.Server, texts []string) ([]string, []string) {
	refs := []string{}
	gaps := []string{}
	if len(texts) == 0 {
		return refs, append(gaps, "turn returned no structured findings")
	}
	for ti, text := range texts {
		output, err := parseDiscoveryOutput(text)
		if err != nil {
			gaps = append(gaps, fmt.Sprintf("findings %d dropped: %s", ti+1, err))
			continue
		}
		for _, gap := range output.Gaps {
			if strings.TrimSpace(gap) != "" {
				gaps = append(gaps, "turn reported: "+strings.TrimSpace(gap))
			}
		}
		for si, sighting := range output.Vacancies {
			ref, gap := materializeSighting(ctx, server, ti, si, sighting)
			if gap != "" {
				gaps = append(gaps, gap)
				continue
			}
			refs = append(refs, ref)
		}
	}
	return refs, gaps
}

// materializeSighting verifies one sighting: re-report of an already-saved
// URL reuses its ref without re-fetching; otherwise the app fetches the
// page itself and saves the sighting bound to the trusted receipt.
func materializeSighting(ctx context.Context, server *publicresearch.Server, ti, si int, sighting discoverySighting) (string, string) {
	label := fmt.Sprintf("vacancy %d", si+1)
	if ti > 0 {
		label = fmt.Sprintf("findings %d vacancy %d", ti+1, si+1)
	}
	pageURL := strings.TrimSpace(sighting.PageURL)
	if pageURL == "" {
		return "", label + " dropped: page_url is required"
	}
	if vac, ok := server.VacancyByURL(pageURL); ok {
		return vac.VacancyRef, ""
	}
	fetched, err := server.FetchURL(ctx, pageURL)
	if err != nil {
		return "", label + " dropped: " + err.Error()
	}
	vac, _, err := server.SaveVacancy(ctx, publicresearch.SaveVacancyInput{
		PageURL: pageURL, EmployerName: sighting.EmployerName, Title: sighting.Title,
		LocationText: sighting.LocationText, PostedText: sighting.PostedText,
		Receipt: fetched.Receipt.ID,
	})
	if err != nil {
		return "", label + " dropped: " + err.Error()
	}
	return vac.VacancyRef, ""
}

// mergeSavedRefs appends materialized refs to the terminal set, skipping
// refs the run already holds (harness-emitted or resume-carried).
func mergeSavedRefs(held []string, added []string) []string {
	seen := make(map[string]bool, len(held)+len(added))
	for _, ref := range held {
		seen[ref] = true
	}
	out := append([]string(nil), held...)
	for _, ref := range added {
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}
