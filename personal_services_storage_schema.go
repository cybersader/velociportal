package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"unicode/utf8"
)

const maxPersonalSnapshotBytes = 9 * 1024 * 1024

// Disk-only authored state. A record means initialized, including an empty
// document. These ownership bindings are NOT authorization evidence. A later
// adapter must check fresh authoritative ownership before every private use.
// StoreID survives restart; a future process CAS epoch must remain separate.
type personalStorageSnapshot struct {
	Version     int                         `json:"version"`
	Namespace   string                      `json:"namespace"`
	StoreID     string                      `json:"store_id"`
	Collections []personalStorageCollection `json:"collections"`
}

type personalStorageCollection struct {
	Login    string                   `json:"login"`
	UserID   string                   `json:"user_id"`
	Revision string                   `json:"revision"`
	Document PersonalServicesDocument `json:"document"`
}

func validPersonalNamespace(value string) bool {
	return validStateNamespace(value)
}

func validPersonalStoreID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validatePersonalSnapshot(snapshot personalStorageSnapshot) error {
	if snapshot.Version != 1 || !validPersonalNamespace(snapshot.Namespace) || !validPersonalStoreID(snapshot.StoreID) || snapshot.Collections == nil {
		return errPersonalInvalid
	}
	if len(snapshot.Collections) > maxPersonalCollections {
		return errPersonalCapacity
	}
	logins, ids := map[string]bool{}, map[string]bool{}
	for _, record := range snapshot.Collections {
		if !validPersonalLogin(record.Login) || logins[record.Login] || !validTailscaleUserID(record.UserID) || ids[record.UserID] {
			return errPersonalInvalid
		}
		logins[record.Login], ids[record.UserID] = true, true
		revision, err := strconv.ParseUint(record.Revision, 10, 64)
		if err != nil || revision == 0 || strconv.FormatUint(revision, 10) != record.Revision {
			return errPersonalInvalid
		}
		if err := validatePersonalDocument(record.Document); err != nil {
			return err
		}
	}
	return nil
}

func copyPersonalSnapshot(snapshot personalStorageSnapshot) personalStorageSnapshot {
	out := snapshot
	out.Collections = make([]personalStorageCollection, len(snapshot.Collections))
	for i, record := range snapshot.Collections {
		out.Collections[i] = record
		out.Collections[i].Document = copyPersonalDocument(record.Document)
	}
	return out
}

// Bound nesting before the existing duplicate-field walker recurses. Strings
// and escapes cannot smuggle delimiters into the depth count. json.Valid runs
// first, so the scalar escape validator can safely inspect complete escapes.
func personalSnapshotJSON(data []byte) bool {
	if len(data) > maxPersonalSnapshotBytes || !utf8.Valid(data) || !json.Valid(data) {
		return false
	}
	depth, quoted, escaped := 0, false, false
	for _, c := range data {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 16 {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return personalJSONScalars(data) && rejectDuplicateJSONFields(data) == nil
}

func parsePersonalSnapshot(data []byte, namespace string) (personalStorageSnapshot, error) {
	var snapshot personalStorageSnapshot
	if !validPersonalNamespace(namespace) || !personalSnapshotJSON(data) {
		return snapshot, errPersonalInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 4 || !containsOnlyJSONFields(fields, "version", "namespace", "store_id", "collections") || !bytes.Equal(bytes.TrimSpace(fields["version"]), []byte("1")) {
		return snapshot, errPersonalInvalid
	}
	for _, key := range []string{"namespace", "store_id"} {
		if _, ok := personalJSONString(fields[key]); !ok {
			return snapshot, errPersonalInvalid
		}
	}
	var records []map[string]json.RawMessage
	if json.Unmarshal(fields["collections"], &records) != nil || records == nil || len(records) > maxPersonalCollections {
		return snapshot, errPersonalInvalid
	}
	for _, record := range records {
		if len(record) != 4 || !containsOnlyJSONFields(record, "login", "user_id", "revision", "document") || !strictPersonalDocument(record["document"]) {
			return snapshot, errPersonalInvalid
		}
		for _, key := range []string{"login", "user_id", "revision"} {
			if _, ok := personalJSONString(record[key]); !ok {
				return snapshot, errPersonalInvalid
			}
		}
	}
	if json.Unmarshal(data, &snapshot) != nil || snapshot.Namespace != namespace {
		return personalStorageSnapshot{}, errPersonalInvalid
	}
	if err := validatePersonalSnapshot(snapshot); err != nil {
		return personalStorageSnapshot{}, err
	}
	return copyPersonalSnapshot(snapshot), nil
}

func serializePersonalSnapshot(snapshot personalStorageSnapshot) ([]byte, error) {
	// Bound collections, fields and each complete document before marshaling the
	// whole file. No unbounded caller slice/string is copied or marshaled first.
	if err := validatePersonalSnapshot(snapshot); err != nil {
		return nil, err
	}
	snapshot = copyPersonalSnapshot(snapshot)
	sort.Slice(snapshot.Collections, func(i, j int) bool { return snapshot.Collections[i].Login < snapshot.Collections[j].Login })
	data, err := json.Marshal(snapshot)
	if err != nil || len(data)+1 > maxPersonalSnapshotBytes {
		return nil, errPersonalCapacity
	}
	return append(data, '\n'), nil
}
