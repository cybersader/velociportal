package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func storageTestSnapshot() personalStorageSnapshot {
	return personalStorageSnapshot{Version: 1, Namespace: "fixture-1", StoreID: strings.Repeat("a", 32), Collections: []personalStorageCollection{
		{Login: "bob@example.com", UserID: "api:user-b", Revision: "18446744073709551615", Document: PersonalServicesDocument{Version: 1, Entries: []PersonalService{}}},
		{Login: "alice@example.com", UserID: "user-α", Revision: "1", Document: PersonalServicesDocument{Version: 1, Entries: []PersonalService{{ID: "ps_" + strings.Repeat("1", 32), Name: "Fixture", URL: "https://server.tailnet.ts.net/app", Icon: "generic"}}}},
	}}
}

func storageTestBytes(t *testing.T) []byte {
	t.Helper()
	data, err := serializePersonalSnapshot(storageTestSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPersonalStorageSchemaRoundTripCopiesAndEmpty(t *testing.T) {
	original := storageTestSnapshot()
	data := storageTestBytes(t)
	if strings.Index(string(data), "alice@example.com") > strings.Index(string(data), "bob@example.com") {
		t.Fatal("not sorted")
	}
	if original.Collections[0].Login != "bob@example.com" {
		t.Fatal("sorted caller memory")
	}
	parsed, err := parsePersonalSnapshot(data, original.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.StoreID != original.StoreID || len(parsed.Collections) != 2 || parsed.Collections[1].Document.Entries == nil || len(parsed.Collections[1].Document.Entries) != 0 || parsed.Collections[1].Revision != "18446744073709551615" {
		t.Fatal("lost initialized empty / ID / revision")
	}
	again, err := serializePersonalSnapshot(parsed)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("nondeterministic", err)
	}
	cloned := copyPersonalSnapshot(parsed)
	cloned.Collections[0].Document.Entries[0].Name = "Changed"
	cloned.Collections[1].UserID = "changed"
	if parsed.Collections[0].Document.Entries[0].Name != "Fixture" || parsed.Collections[1].UserID != "api:user-b" {
		t.Fatal("aliased copy")
	}
	for i := range data {
		data[i] = ' '
	}
	if parsed.Collections[0].Login != "alice@example.com" {
		t.Fatal("aliased raw data")
	}
	absent := original
	absent.Collections = []personalStorageCollection{}
	encoded, err := serializePersonalSnapshot(absent)
	if err != nil {
		t.Fatal(err)
	}
	result, err := parsePersonalSnapshot(encoded, absent.Namespace)
	if err != nil || result.Collections == nil || len(result.Collections) != 0 {
		t.Fatal("absent roundtrip", err)
	}
}

func TestPersonalStorageSchemaStrictJSON(t *testing.T) {
	valid := string(storageTestBytes(t))
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valid), &top); err != nil {
		t.Fatal(err)
	}
	cases := []string{"null", "[]", "{}", valid + "{}", strings.Replace(valid, `"version":1`, `"version":1.0`, 1), strings.Replace(valid, `"version":1`, `"version":2`, 1), strings.Replace(valid, `"version":1`, `"version":1,"Version":1`, 1), strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(valid, `"namespace"`, `"Namespace"`, 1), strings.Replace(valid, `"store_id"`, `"store_id"`, 1) + "null"}
	for key := range top {
		for _, replacement := range []string{"missing", "null", "alias", "duplicate", "unknown"} {
			fields := map[string]json.RawMessage{}
			for k, v := range top {
				fields[k] = v
			}
			switch replacement {
			case "missing":
				delete(fields, key)
			case "null":
				fields[key] = json.RawMessage("null")
			case "alias":
				fields[strings.ToUpper(key)] = fields[key]
				delete(fields, key)
			case "duplicate":
				fields[strings.ToUpper(key)] = fields[key]
			case "unknown":
				fields["extra"] = json.RawMessage("true")
			}
			b, _ := json.Marshal(fields)
			cases = append(cases, string(b))
		}
	}
	for _, key := range []string{"login", "user_id", "revision", "document"} {
		var records []map[string]json.RawMessage
		_ = json.Unmarshal(top["collections"], &records)
		for _, mutation := range []string{"missing", "null", "alias", "duplicate", "unknown"} {
			var copied []map[string]json.RawMessage
			b, _ := json.Marshal(records)
			_ = json.Unmarshal(b, &copied)
			switch mutation {
			case "missing":
				delete(copied[0], key)
			case "null":
				copied[0][key] = json.RawMessage("null")
			case "alias":
				copied[0][strings.ToUpper(key)] = copied[0][key]
				delete(copied[0], key)
			case "duplicate":
				copied[0][strings.ToUpper(key)] = copied[0][key]
			case "unknown":
				copied[0]["extra"] = json.RawMessage("0")
			}
			changed := map[string]json.RawMessage{}
			for k, v := range top {
				changed[k] = v
			}
			changed["collections"], _ = json.Marshal(copied)
			b, _ = json.Marshal(changed)
			cases = append(cases, string(b))
		}
	}
	for _, pair := range [][2]string{
		{`"entries":[{`, `"entries":[null,{`}, {`"entries":[{`, `"entries":null,"extra":[{`},
		{`"entries":[{`, `"entries":[{"extra":null,`}, {`"name":"Fixture"`, `"Name":"Fixture"`},
		{`"name":"Fixture"`, `"name":null`}, {`"name":"Fixture"`, `"name":"Fixture","name":"Other"`},
		{`"name":"Fixture"`, `"name":"\ud800"`}, {`"icon":"generic"`, `"icon":null`},
		{`"icon":"generic"`, `"icon":"generic","Icon":"generic"`},
		{`"document":{"version":1`, `"document":{"Version":1`},
		{`"name":"Fixture"`, `"name":` + strings.Repeat("[", 17) + "0" + strings.Repeat("]", 17)},
	} {
		cases = append(cases, strings.Replace(valid, pair[0], pair[1], 1))
	}
	cases = append(cases, strings.Replace(valid, "Fixture", string([]byte{255}), 1))
	for i, source := range cases {
		if out, err := parsePersonalSnapshot([]byte(source), "fixture-1"); err == nil || !reflect.DeepEqual(out, personalStorageSnapshot{}) {
			t.Fatalf("accepted invalid case %d", i)
		}
	}
	if _, err := parsePersonalSnapshot([]byte(valid), "other"); err == nil {
		t.Fatal("namespace mismatch")
	}
	// Valid surrogate pairs and escaped JSON keys do not count as duplicates
	// unless their decoded names collide.
	slash := string(rune(92))
	paired := strings.Replace(valid, "Fixture", slash+"ud83d"+slash+"ude00", 1)
	if _, err := parsePersonalSnapshot([]byte(paired), "fixture-1"); err != nil {
		t.Fatal("valid Unicode pair", err)
	}
	escaped := strings.Replace(valid, `"store_id"`, `"store_`+slash+`u0069d"`, 1)
	if _, err := parsePersonalSnapshot([]byte(escaped), "fixture-1"); err != nil {
		t.Fatal("decoded canonical field", err)
	}
	duplicate := strings.Replace(valid, `"store_id":`, `"store_`+slash+`u0069d":"`+strings.Repeat("a", 32)+`","store_id":`, 1)
	if _, err := parsePersonalSnapshot([]byte(duplicate), "fixture-1"); err == nil {
		t.Fatal("escaped duplicate")
	}
}

func TestPersonalStorageSchemaBindingsAndRecords(t *testing.T) {
	for _, namespace := range []string{"", " A", "A", "a ", "_a", "-a", "a.b", "é", strings.Repeat("a", 65)} {
		s := storageTestSnapshot()
		s.Namespace = namespace
		if _, err := serializePersonalSnapshot(s); err == nil {
			t.Fatalf("namespace %q", namespace)
		}
	}
	for _, namespace := range []string{"0", "a", "a_0-z", strings.Repeat("a", 64)} {
		s := storageTestSnapshot()
		s.Namespace = namespace
		b, err := serializePersonalSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parsePersonalSnapshot(b, namespace); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*personalStorageSnapshot){
		func(s *personalStorageSnapshot) { s.Version = 2 },
		func(s *personalStorageSnapshot) { s.Collections = nil },
		func(s *personalStorageSnapshot) { s.StoreID = strings.Repeat("A", 32) },
		func(s *personalStorageSnapshot) { s.StoreID = strings.Repeat("a", 31) },
		func(s *personalStorageSnapshot) { s.StoreID = strings.Repeat("g", 32) },
		func(s *personalStorageSnapshot) { s.Collections[0].Login = s.Collections[1].Login },
		func(s *personalStorageSnapshot) { s.Collections[0].UserID = s.Collections[1].UserID },
		func(s *personalStorageSnapshot) { s.Collections[1].Document.Entries[0].ID = "ps_bad" },
		func(s *personalStorageSnapshot) { s.Collections[1].Document.Entries[0].Name = "" },
		func(s *personalStorageSnapshot) { s.Collections[1].Document.Entries[0].URL = "https://example.com" },
		func(s *personalStorageSnapshot) { s.Collections[1].Document.Entries[0].Icon = "unknown" },
		func(s *personalStorageSnapshot) { s.Collections[0].Document.Entries = nil },
		func(s *personalStorageSnapshot) { s.Collections[0].Document.Version = 2 },
	} {
		s := storageTestSnapshot()
		change(&s)
		if _, err := serializePersonalSnapshot(s); err == nil {
			t.Fatal("invalid candidate accepted")
		}
	}
	for _, login := range []string{"alice", "alice@", " alice@example.com", "alice@example.com ", "alice\n@example.com", "alice​@example.com", strings.Repeat("a", 250) + "@test.com"} {
		s := storageTestSnapshot()
		s.Collections[0].Login = login
		if _, err := serializePersonalSnapshot(s); err == nil {
			t.Fatalf("login %q", login)
		}
	}
	for _, id := range []string{"", " padded", "padded ", "bad\nvalue", string([]byte{255}), strings.Repeat("x", 257)} {
		s := storageTestSnapshot()
		s.Collections[0].UserID = id
		if _, err := serializePersonalSnapshot(s); err == nil {
			t.Fatal("invalid user ID")
		}
	}
	for _, revision := range []string{"", "0", "01", "+1", "-1", " 1", "1 ", "1.0", "1e1", "18446744073709551616"} {
		s := storageTestSnapshot()
		s.Collections[0].Revision = revision
		if _, err := serializePersonalSnapshot(s); err == nil {
			t.Fatalf("revision %q", revision)
		}
	}
	s := storageTestSnapshot()
	s.Collections[0].Login = "Bob@example.com"
	s.Collections[0].UserID = strings.Repeat("x", 256)
	b, err := serializePersonalSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parsePersonalSnapshot(b, s.Namespace)
	if err != nil || parsed.Collections[0].Login != "Bob@example.com" {
		t.Fatal("normalized exact binding", err)
	}
}

func storageCapacitySnapshot(t *testing.T) personalStorageSnapshot {
	t.Helper()
	s := storageTestSnapshot()
	s.Collections = make([]personalStorageCollection, maxPersonalCollections)
	// A complete document just below 64KiB, with maximum entry count. Entry
	// ordering is authored and must not be sorted by persistence.
	d := PersonalServicesDocument{Version: 1, Entries: make([]PersonalService, maxPersonalEntries)}
	for i := range d.Entries {
		d.Entries[i] = PersonalService{ID: fmt.Sprintf("ps_%032x", maxPersonalEntries-i), Name: "Fixture", URL: "https://server.tailnet.ts.net/" + strings.Repeat("a", 400)}
	}
	for i := range s.Collections {
		s.Collections[i] = personalStorageCollection{Login: fmt.Sprintf("user%03d@example.com", i), UserID: fmt.Sprintf("user:%03d", i), Revision: "1", Document: copyPersonalDocument(d)}
	}
	return s
}

func TestPersonalStorageSchemaCapacityAndBounds(t *testing.T) {
	s := storageCapacitySnapshot(t)
	b, err := serializePersonalSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 7*1024*1024 || len(b) > maxPersonalSnapshotBytes {
		t.Fatalf("capacity fixture size %d", len(b))
	}
	parsed, err := parsePersonalSnapshot(b, s.Namespace)
	if err != nil || !reflect.DeepEqual(parsed, s) {
		t.Fatal("capacity roundtrip", err)
	}
	s.Collections = append(s.Collections, s.Collections[0])
	if _, err := serializePersonalSnapshot(s); err == nil {
		t.Fatal("129 collections")
	}
	s = storageTestSnapshot()
	s.Collections[1].Document.Entries = make([]PersonalService, 129)
	if _, err := serializePersonalSnapshot(s); err == nil {
		t.Fatal("129 entries")
	}
	s = storageCapacitySnapshot(t)
	for i := range s.Collections[0].Document.Entries {
		s.Collections[0].Document.Entries[i].URL = "https://server.tailnet.ts.net/" + strings.Repeat("a", 500)
	}
	if _, err := serializePersonalSnapshot(s); err == nil {
		t.Fatal("oversized complete document")
	}
	if _, err := parsePersonalSnapshot(bytes.Repeat([]byte(" "), maxPersonalSnapshotBytes+1), "fixture-1"); err == nil {
		t.Fatal("oversized raw snapshot")
	}
	valid := storageTestBytes(t)
	padded := append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), maxPersonalSnapshotBytes-len(valid))...)
	if _, err := parsePersonalSnapshot(padded, "fixture-1"); err != nil {
		t.Fatal("exact raw limit", err)
	}
	padded = append(padded, ' ')
	if _, err := parsePersonalSnapshot(padded, "fixture-1"); err == nil {
		t.Fatal("raw limit + 1")
	}
}
