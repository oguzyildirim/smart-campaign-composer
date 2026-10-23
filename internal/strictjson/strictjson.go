// Package strictjson decodes untrusted JSON strictly. encoding/json alone
// accepts duplicate object keys (keeping the last one), trailing data, and
// unknown struct fields; for input from clients or an LLM each of those can
// hide a value from a reviewer or smuggle in an unexpected one.
//
// Error messages never echo input values, so they are safe to return to
// clients, log, and feed back to an LLM.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ErrDuplicateKey is reported when an object repeats a key.
var ErrDuplicateKey = errors.New("duplicate object key")

// Error describes why a document was rejected. Path is a dotted location
// such as "sms.body" ("$" for the whole document).
type Error struct {
	Path    string
	Message string
}

func (e *Error) Error() string { return e.Path + ": " + e.Message }

// CheckSyntax verifies that data is exactly one well-formed JSON value
// with no duplicate object keys and nothing after it.
func CheckSyntax(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkValue(dec); err != nil {
		if errors.Is(err, ErrDuplicateKey) {
			return err
		}
		return errors.New("malformed JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON value")
	}
	return nil
}

// Decode checks syntax like CheckSyntax and then decodes data into v,
// rejecting object keys that v does not declare. Every failure is an
// *Error.
func Decode(data []byte, v any) error {
	if err := CheckSyntax(data); err != nil {
		return &Error{Path: "$", Message: err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		return nil
	}
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		path := typeErr.Field
		if path == "" {
			path = "$"
		}
		return &Error{Path: path, Message: "has the wrong JSON type"}
	}
	// encoding/json reports unknown fields only as text: `json: unknown
	// field "name"`. The key itself is not echoed back.
	if strings.HasPrefix(err.Error(), "json: unknown field") {
		return &Error{Path: "$", Message: "contains an unknown field"}
	}
	return &Error{Path: "$", Message: "could not be decoded"}
}

func walkValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar
	}
	switch delim {
	case '{':
		keys := make(map[string]bool)
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			if keys[key] {
				return ErrDuplicateKey
			}
			keys[key] = true
			if err := walkValue(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkValue(dec); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // closing delimiter
	return err
}
