package agency

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrUnsupportedOwnerSource = errors.New("unsupported owner vacancy URL")

type OwnerSourceReader interface {
	ReadVacancy(context.Context, string) (string, error)
}

type LeverOwnerSourceReader struct{ HTTPClient *http.Client }

func supportedLeverOwnerURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Host != "jobs.lever.co" && u.Host != "jobs.eu.lever.co") {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(parts[0]) > 100 || len(parts[1]) > 100 {
		return false
	}
	for _, part := range parts {
		for _, char := range part {
			if char != '-' && char != '_' && (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') {
				return false
			}
		}
	}
	return true
}

func (r LeverOwnerSourceReader) ReadVacancy(ctx context.Context, rawURL string) (string, error) {
	if !supportedLeverOwnerURL(rawURL) {
		return "", ErrUnsupportedOwnerSource
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrUnsupportedOwnerSource
	}
	apiHost := ""
	switch u.Host {
	case "jobs.lever.co":
		apiHost = "api.lever.co"
	case "jobs.eu.lever.co":
		apiHost = "api.eu.lever.co"
	default:
		return "", ErrUnsupportedOwnerSource
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(parts[0]) > 100 || len(parts[1]) > 100 {
		return "", ErrUnsupportedOwnerSource
	}
	for _, part := range parts {
		for _, char := range part {
			if char != '-' && char != '_' && (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') {
				return "", ErrUnsupportedOwnerSource
			}
		}
	}
	endpoint := "https://" + apiHost + "/v0/postings/" + parts[0] + "/" + parts[1]
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if r.HTTPClient != nil {
		clone := *r.HTTPClient
		if clone.Timeout <= 0 || clone.Timeout > 15*time.Second {
			clone.Timeout = 15 * time.Second
		}
		clone.CheckRedirect = client.CheckRedirect
		client = &clone
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "" && !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return "", ErrUnsupportedOwnerSource
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 200001))
	if err != nil || len(body) == 0 || len(body) > 200000 {
		return "", ErrUnsupportedOwnerSource
	}
	var posting struct {
		ID               string `json:"id"`
		Title            string `json:"text"`
		DescriptionPlain string `json:"descriptionPlain"`
		OpeningPlain     string `json:"openingPlain"`
		HostedURL        string `json:"hostedUrl"`
	}
	if json.Unmarshal(body, &posting) != nil || posting.ID != parts[1] || posting.HostedURL != rawURL ||
		strings.TrimSpace(posting.Title) == "" || strings.TrimSpace(posting.DescriptionPlain) == "" && strings.TrimSpace(posting.OpeningPlain) == "" {
		return "", ErrUnsupportedOwnerSource
	}
	return string(body), nil
}
