package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// This optional raw view never replaces the incumbent Devices decoder. Ambiguity
// removes presentation evidence only; legacy IDs, completeness and errors remain
// authoritative for the snapshot. The response byte bound also bounds this view.
func tailscalePresentationDeviceIDs(body []byte, count int) []ProviderDeviceID {
	if len(body) > maxUpstreamResponseBody || !json.Valid(body) {
		return nil
	}
	envelope, ok := presentationIdentityFields(body, "devices")
	if !ok {
		return nil
	}
	var records []json.RawMessage
	if json.Unmarshal(envelope["devices"], &records) != nil || len(records) != count {
		return nil
	}
	ids := make([]ProviderDeviceID, count)
	for i, record := range records {
		ids[i] = tailscalePresentationDeviceID(record)
	}
	return ids
}

// Read only the named fields, detecting their duplicates and case aliases before
// encoding/json could discard provenance. Unknown profile fields (even repeated
// ones) remain ignored. Raw values avoid floating-point or Unicode coercion.
func presentationIdentityFields(raw []byte, names ...string) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		for _, name := range names {
			if !strings.EqualFold(key, name) {
				continue
			}
			if _, exists := fields[name]; exists || key != name {
				return nil, false
			}
			fields[name] = value
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func tailscalePresentationDeviceID(record []byte) ProviderDeviceID {
	fields, ok := presentationIdentityFields(record, "id", "nodeId")
	if !ok {
		return ProviderDeviceID{}
	}
	// Reject invalid identity scalars, including a lossy alternative field. Do not
	// validate unused profile data or change the Devices API's error behavior.
	for _, raw := range fields {
		if !utf8.Valid(raw) || !json.Valid(raw) || !personalJSONScalars(raw) {
			return ProviderDeviceID{}
		}
	}
	raw := bytes.TrimSpace(fields["id"])
	kind := deviceIDKindID
	value := ""
	fallback := len(raw) == 0 || bytes.Equal(raw, []byte("null"))
	if !fallback {
		if raw[0] == '"' {
			if json.Unmarshal(raw, &value) != nil {
				return ProviderDeviceID{}
			}
			fallback = value == "" // Not a trimmed whitespace-only value.
		} else {
			// JSON numeric id: exact nonnegative decimal integer, at most 128
			// digits. No sign, exponent, fraction, rounding or leading zero.
			if len(raw) > 128 || (len(raw) > 1 && raw[0] == '0') {
				return ProviderDeviceID{}
			}
			for _, c := range raw {
				if c < '0' || c > '9' {
					return ProviderDeviceID{}
				}
			}
			value = string(raw)
		}
	}
	if fallback {
		kind = deviceIDKindNodeID
		if json.Unmarshal(fields["nodeId"], &value) != nil {
			return ProviderDeviceID{}
		}
	}
	if !validPresentationDeviceID(kind, value) {
		return ProviderDeviceID{}
	}
	return ProviderDeviceID{Provider: controlPlaneTailscale, Kind: kind, Value: value}
}

// No first/last winner for any unforeseen collision. The normal loader already
// rejects duplicate legacy node IDs; this side-map additionally omits every
// colliding typed association, without trying an alternative ID or hostname.
func presentationDeviceIDsByNodeID(nodes []Node, ids []ProviderDeviceID) map[string]ProviderDeviceID {
	if len(nodes) != len(ids) {
		return nil
	}
	typedCounts := make(map[ProviderDeviceID]int, len(ids))
	nodeCounts := make(map[string]int, len(nodes))
	for i, id := range ids {
		typedCounts[id]++
		nodeCounts[nodes[i].ID]++
	}
	var result map[string]ProviderDeviceID
	for i, id := range ids {
		if id.Provider != controlPlaneTailscale || !validPresentationDeviceID(id.Kind, id.Value) || typedCounts[id] != 1 || nodeCounts[nodes[i].ID] != 1 || nodes[i].ID == "" {
			continue
		}
		if result == nil {
			result = make(map[string]ProviderDeviceID)
		}
		result[nodes[i].ID] = id
	}
	return result
}
