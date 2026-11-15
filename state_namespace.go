package main

import "errors"

// The deployment/source namespace is an operator assertion, not an upstream ID
// or an authorization input. Keep the incumbent private persisted grammar.
func validStateNamespace(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for i, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-')) {
			return false
		}
	}
	return true
}

// Read exactly once, even when personal services and shared editing are off.
// Consumer validation follows personal configuration, without opening any file.
func loadStateNamespace(lookup configLookup) (string, error) {
	value, present, err := lookup("PORTAL_STATE_NAMESPACE")
	if err != nil {
		return "", errors.New("PORTAL_STATE_NAMESPACE could not be read")
	}
	if !present {
		return "", nil
	}
	if !validStateNamespace(value) {
		return "", errors.New("PORTAL_STATE_NAMESPACE must be a canonical namespace")
	}
	return value, nil
}
