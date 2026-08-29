package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseServiceMetadata(t *testing.T) {
	metadata, err := parseServiceMetadata([]byte(`{
		"version": 1,
		"services": [
			{"proxy_host_id": 42, "name": "Rader Wiki", "url": "https://wiki.rader.wiki/path?q=1"},
			{"proxy_host_id": 43, "url": "http://home.home:8080/"}
		]
	}`))
	if err != nil {
		t.Fatalf("parseServiceMetadata() error = %v", err)
	}
	if got := metadata.Overrides[42]; got.Name != "Rader Wiki" || got.URL != "https://wiki.rader.wiki/path?q=1" {
		t.Fatalf("override 42 = %#v", got)
	}
	if got := metadata.Overrides[43]; got.Name != "" || got.URL != "http://home.home:8080/" {
		t.Fatalf("override 43 = %#v", got)
	}
}

func TestParseServiceMetadataRejectsInvalidDocumentsWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name     string
		document string
		contains string
	}{
		{name: "empty", document: ``, contains: "empty"},
		{name: "null", document: `null`, contains: "version"},
		{name: "wrong version", document: `{"version":2,"services":[]}`, contains: "version"},
		{name: "missing services", document: `{"version":1}`, contains: "array"},
		{name: "null services", document: `{"version":1,"services":null}`, contains: "array"},
		{name: "unknown field", document: `{"version":1,"services":[],"other":true}`, contains: "invalid"},
		{name: "duplicate field", document: `{"version":1,"version":1,"services":[]}`, contains: "duplicate field"},
		{name: "mixed-case duplicate top field", document: `{"version":2,"Version":1,"services":[]}`, contains: "duplicate field"},
		{name: "mixed-case duplicate entry field", document: `{"version":1,"services":[{"proxy_host_id":1,"url":"https://safe.example/","URL":"https://secret.example/"}]}`, contains: "duplicate field"},
		{name: "noncanonical top field", document: `{"Version":1,"services":[]}`, contains: "invalid"},
		{name: "noncanonical entry field", document: `{"version":1,"services":[{"proxy_host_id":1,"URL":"https://secret.example/"}]}`, contains: "invalid"},
		{name: "trailing", document: `{"version":1,"services":[]} {}`, contains: "trailing"},
		{name: "invalid id", document: `{"version":1,"services":[{"proxy_host_id":0,"name":"x"}]}`, contains: "proxy_host_id"},
		{name: "duplicate target", document: `{"version":1,"services":[{"proxy_host_id":1,"name":"x"},{"proxy_host_id":1,"name":"y"}]}`, contains: "duplicates"},
		{name: "empty override", document: `{"version":1,"services":[{"proxy_host_id":1}]}`, contains: "name or url"},
		{name: "padded name", document: `{"version":1,"services":[{"proxy_host_id":1,"name":" secret-name "}]}`, contains: "invalid name"},
		{name: "control name", document: "{\"version\":1,\"services\":[{\"proxy_host_id\":1,\"name\":\"secret-name\\nvalue\"}]}", contains: "invalid name"},
		{name: "wildcard url", document: `{"version":1,"services":[{"proxy_host_id":1,"url":"https://*.secret.example/"}]}`, contains: "invalid url"},
		{name: "userinfo url", document: `{"version":1,"services":[{"proxy_host_id":1,"url":"https://user:secret@example.com/"}]}`, contains: "invalid url"},
		{name: "relative url", document: `{"version":1,"services":[{"proxy_host_id":1,"url":"/secret/path"}]}`, contains: "invalid url"},
		{name: "non-http url", document: `{"version":1,"services":[{"proxy_host_id":1,"url":"ssh://secret.example/"}]}`, contains: "invalid url"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServiceMetadata([]byte(test.document))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("parseServiceMetadata() error = %v, want %q", err, test.contains)
			}
			for _, private := range []string{"secret-name", "secret.example", "secret/path", "user:secret"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("error leaked %q: %v", private, err)
				}
			}
		})
	}
}

func TestParseServiceMetadataRejectsLimits(t *testing.T) {
	longName := strings.Repeat("x", maxServiceMetadataName+1)
	_, err := parseServiceMetadata([]byte(`{"version":1,"services":[{"proxy_host_id":1,"name":"` + longName + `"}]}`))
	if err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Fatalf("long name error = %v", err)
	}

	entries := make([]string, maxServiceMetadataEntries+1)
	for index := range entries {
		entries[index] = `{"proxy_host_id":` + string(rune('1'+index%9)) + `,"name":"x"}`
	}
	// Count validation runs before duplicate-target validation.
	_, err = parseServiceMetadata([]byte(`{"version":1,"services":[` + strings.Join(entries, ",") + `]}`))
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("entry limit error = %v", err)
	}
}

func TestLoadServiceMetadataFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := loadServiceMetadataFile(path)
	if err != nil {
		t.Fatalf("loadServiceMetadataFile() error = %v", err)
	}
	if len(metadata.Overrides) != 0 {
		t.Fatalf("overrides = %#v", metadata.Overrides)
	}

	_, err = loadServiceMetadataFile(filepath.Join(dir, "missing-private-name.json"))
	if err == nil || strings.Contains(err.Error(), "missing-private-name") {
		t.Fatalf("missing file error = %v", err)
	}

	_, err = loadServiceMetadataFile(dir)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
}

func TestServiceMetadataLoaderForBlankPath(t *testing.T) {
	metadata, err := serviceMetadataLoaderForPath("  ")()
	if err != nil || metadata == nil || len(metadata.Overrides) != 0 {
		t.Fatalf("blank loader = %#v, %v", metadata, err)
	}
}

func TestUnmatchedServiceMetadataCount(t *testing.T) {
	metadata := &ServiceMetadata{Overrides: map[int]ServiceOverride{1: {}, 2: {}, 3: {}}}
	if got := unmatchedServiceMetadataCount(metadata, []ProxyHost{{ID: 1}, {ID: 3}}); got != 1 {
		t.Fatalf("unmatchedServiceMetadataCount() = %d", got)
	}
}
