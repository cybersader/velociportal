package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Users data becomes ownership evidence. Reject lossy text before encoding/json
// can replace invalid UTF-8 or unpaired UTF-16 escapes. The scalar checker is
// shared with private authored records; it requires syntactically valid JSON.
func losslessTailscaleUsersJSON(data []byte) bool {
	return utf8.Valid(data) && json.Valid(data) && personalJSONScalars(data)
}

// Decode one object without last-field-wins or encoding/json's case-insensitive
// struct-field matching. Unused upstream profile fields remain supported.
func tailscaleUsersObject(raw []byte, canonical ...string) (map[string]json.RawMessage, error) {
	invalid := errors.New("ambiguous or invalid users object")
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, invalid
		}
		if _, exists := fields[key]; exists {
			return nil, invalid
		}
		for _, name := range canonical {
			if strings.EqualFold(key, name) && key != name {
				return nil, invalid
			}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, invalid
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, invalid
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, invalid
	}
	return fields, nil
}

// Shared by upstream ownership evidence and persisted bindings. Upstream IDs
// first use the established tailscaleReference normalization (including numeric
// and opaque forms); persisted IDs must already be normalized and compare exact.
func validTailscaleUserID(id string) bool {
	return len(id) > 0 && len(id) <= 256 && utf8.ValidString(id) && strings.TrimSpace(id) == id && !containsControl(id)
}
