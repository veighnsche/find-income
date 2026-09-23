package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (h *Handler) listApplicationPacks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	opportunityID := r.PathValue("id")
	if _, err := h.database.Opportunity(r.Context(), opportunityID); err != nil {
		failPack(w, err)
		return
	}
	items, err := h.database.ListApplicationPacks(r.Context(), opportunityID)
	if err != nil {
		failPack(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []store.ApplicationPackSummary `json:"items"`
	}{items})
}

func failPack(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Application pack not found.")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid application pack.")
		return
	}
	fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Application pack could not be read.")
}

func (h *Handler) getApplicationPack(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	pack, err := h.database.ApplicationPack(r.Context(), r.PathValue("id"))
	if err != nil {
		failPack(w, err)
		return
	}
	var manifest json.RawMessage = pack.ManifestJSON
	writeJSON(w, http.StatusOK, struct {
		ID                  string          `json:"id"`
		OpportunityID       string          `json:"opportunityId"`
		OpportunityRevision int64           `json:"opportunityRevision"`
		ProfileRevision     int64           `json:"profileRevision"`
		Version             int64           `json:"version"`
		ContentSHA256       string          `json:"contentSha256"`
		CreatedAt           string          `json:"createdAt"`
		Manifest            json.RawMessage `json:"manifest"`
	}{pack.ID, pack.OpportunityID, pack.OpportunityRevision, pack.ProfileRevision, pack.Version, pack.ContentSHA256, pack.CreatedAt, manifest})
}

func (h *Handler) applicationPackPDF(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	pack, err := h.database.ApplicationPack(r.Context(), r.PathValue("id"))
	if err != nil {
		failPack(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename=application-pack-"+pack.ID+".pdf")
	_, _ = w.Write(pack.PDF)
}

// The stored Typst source references focus.json. Reconstruct that exact data
// from the immutable manifest so the source download compiles independently.
func (h *Handler) applicationPackSourceArchive(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	pack, err := h.database.ApplicationPack(r.Context(), r.PathValue("id"))
	if err != nil {
		failPack(w, err)
		return
	}
	var manifest struct {
		Draft struct {
			Focus struct {
				Text string `json:"text"`
			} `json:"focus"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil || manifest.Draft.Focus.Text == "" {
		failPack(w, store.ErrInvalid)
		return
	}
	focus, _ := json.Marshal(struct {
		Focus string `json:"focus"`
	}{manifest.Draft.Focus.Text})
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, file := range []struct {
		Name string
		Body []byte
	}{{"cv.typ", pack.TypstSource}, {"focus.json", focus}} {
		entry, err := archive.Create(file.Name)
		if err != nil {
			failPack(w, err)
			return
		}
		if _, err := entry.Write(file.Body); err != nil {
			failPack(w, err)
			return
		}
	}
	if err := archive.Close(); err != nil {
		failPack(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=application-pack-"+pack.ID+"-source.zip")
	_, _ = w.Write(output.Bytes())
}
