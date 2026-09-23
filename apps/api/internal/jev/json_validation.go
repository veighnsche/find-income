package jev

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// encoding/json otherwise accepts duplicate object keys by keeping the last
// value. Provider output is untrusted, so reject them at every object depth.
func rejectDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing provider JSON")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid provider JSON key")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate provider JSON key")
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid provider JSON delimiter")
	}
}

// A map[string]float64 silently maps a JSON null to zero. Require an actual
// JSON number for every option, including zero-probability options.
type strictProbabilities map[string]float64

func (p *strictProbabilities) UnmarshalJSON(body []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return errors.New("invalid provider probabilities")
	}
	values := make(strictProbabilities, len(raw))
	for key, item := range raw {
		if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
			return errors.New("null provider probability")
		}
		var value float64
		if err := json.Unmarshal(item, &value); err != nil {
			return errors.New("non-numeric provider probability")
		}
		values[key] = value
	}
	*p = values
	return nil
}
