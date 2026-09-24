package researchcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
)

// Operation is the exact-request operation family (T06 §2).
type Operation string

const (
	OperationSearch  Operation = "search"
	OperationFetch   Operation = "fetch"
	OperationBrowser Operation = "browser_action"
	OperationAPI     Operation = "api_call"
	OperationExec    Operation = "exec"
)

// Valid reports whether o is a known operation.
func (o Operation) Valid() bool {
	switch o {
	case OperationSearch, OperationFetch, OperationBrowser, OperationAPI, OperationExec:
		return true
	}
	return false
}

// CacheScope distinguishes reusable captured bytes from stateful live
// browser contexts (T06 §3). Stateful contexts are never implicitly replayed.
type CacheScope string

const (
	CacheStatelessReusable CacheScope = "stateless_reusable"
	CacheStatefulContext   CacheScope = "stateful_context_bound"
)

// Param is one ordered request parameter. Repeats and order are significant.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Pagination records page/cursor/limit exactly as sent.
type Pagination struct {
	Page   string `json:"page,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  string `json:"limit,omitempty"`
}

// RequestDescriptor is the exact-request descriptor (T06 §2). It carries no
// credential or secret fields by construction; validation rejects
// secret-looking parameter names. Struct field order is the canonical order.
type RequestDescriptor struct {
	Operation     Operation         `json:"operation"`
	Backend       string            `json:"backend"`
	Method        string            `json:"method,omitempty"`
	URLOrQuery    string            `json:"url_or_query"`
	BodySHA256    string            `json:"body_sha256,omitempty"`
	Body          string            `json:"body,omitempty"` // normalized, small bodies only
	Params        []Param           `json:"params,omitempty"`
	SessionFields map[string]string `json:"locale_session_context,omitempty"`
	Pagination    Pagination        `json:"pagination,omitempty"`
}

// secretParamNames are rejected in Params (case-insensitive): secrets must
// never enter descriptors, fingerprints, or logs (T06 §2).
var secretParamNames = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true,
	"api_key": true, "apikey": true, "auth": true, "authorization": true,
	"credential": true, "credentials": true, "private_key": true, "privatekey": true,
}

// Validate checks descriptor shape and rejects secret-bearing parameters.
func (d RequestDescriptor) Validate() error {
	if !d.Operation.Valid() {
		return NewError(OutcomeInvalid, "operation", "unknown operation "+string(d.Operation))
	}
	if d.Backend == "" {
		return NewError(OutcomeInvalid, "backend", "backend is required")
	}
	if d.URLOrQuery == "" {
		return NewError(OutcomeInvalid, "url_or_query", "url_or_query is required")
	}
	for _, p := range d.Params {
		if secretParamNames[strings.ToLower(p.Name)] {
			return NewError(OutcomeInvalid, "params", "secret-bearing parameter "+p.Name+" must not enter the request descriptor")
		}
	}
	return nil
}

// normalizeURL applies the fingerprint normalization allowlist: scheme and
// host case folding only. Path, query, params and fragments are significant.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

// Fingerprint returns sha256(canonical_exact_request_json), lowercase hex.
// Canonical form applies only the normalization allowlist (scheme/host case
// folding); parameter order, repeats, path and query are significant.
func (d RequestDescriptor) Fingerprint() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	canonical := d
	canonical.URLOrQuery = normalizeURL(d.URLOrQuery)
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
