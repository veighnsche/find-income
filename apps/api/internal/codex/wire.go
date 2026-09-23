package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

var integerID = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// IDs are canonical JSON, preserving the distinction between 1 and "1" and
// integer precision beyond JavaScript's safe integer range.
func idKey(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", ErrMalformedFrame
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", ErrMalformedFrame
		}
		b, _ := json.Marshal(s)
		return string(b), nil
	}
	if !integerID.Match(raw) {
		return "", ErrMalformedFrame
	}
	return string(raw), nil
}

type envelope struct {
	id       string
	method   string
	params   json.RawMessage
	result   json.RawMessage
	rpcError *RPCError
}

func decodeFrame(frame []byte) (envelope, error) {
	var out envelope
	d := json.NewDecoder(bytes.NewReader(frame))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return out, ErrMalformedFrame
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return out, ErrMalformedFrame
		}
		key, ok := t.(string)
		if !ok {
			return out, ErrMalformedFrame
		}
		if _, duplicate := fields[key]; duplicate {
			return out, ErrMalformedFrame
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return out, ErrMalformedFrame
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return out, ErrMalformedFrame
	}
	if _, err = d.Token(); err != io.EOF {
		return out, ErrMalformedFrame
	}
	if raw, ok := fields["id"]; ok {
		out.id, err = idKey(raw)
		if err != nil {
			return out, err
		}
	}
	method, hasMethod := fields["method"]
	result, hasResult := fields["result"]
	rpcErr, hasError := fields["error"]
	if hasMethod {
		if hasResult || hasError || json.Unmarshal(method, &out.method) != nil || out.method == "" {
			return out, ErrMalformedFrame
		}
		out.params = fields["params"]
		if len(out.params) == 0 {
			out.params = json.RawMessage(`{}`)
		}
		return out, nil
	}
	if out.id == "" || hasResult == hasError {
		return out, ErrMalformedFrame
	}
	if _, ok := fields["params"]; ok {
		return out, ErrMalformedFrame
	}
	if hasResult {
		out.result = result
		return out, nil
	}
	var detail struct {
		Code    *int    `json:"code"`
		Message *string `json:"message"`
	}
	if json.Unmarshal(rpcErr, &detail) != nil || detail.Code == nil || detail.Message == nil {
		return out, ErrMalformedFrame
	}
	out.rpcError = &RPCError{Code: *detail.Code}
	return out, nil
}
