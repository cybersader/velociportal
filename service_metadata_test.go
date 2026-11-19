package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestParseServiceMetadataV2OrganizationFields(t *testing.T) {
	metadata, err := parseServiceMetadata([]byte(`{
		"version": 2,
		"services": [
			{"proxy_host_id": 42, "name": "Rader Wiki", "category": "Réseau", "order": 0},
			{"proxy_host_id": 43, "category": "Infrastructure", "order": 1000000}
		]
	}`))
	if err != nil {
		t.Fatalf("parseServiceMetadata() error = %v", err)
	}
	first := metadata.Overrides[42]
	if first.Name != "Rader Wiki" || first.Category != "Réseau" || first.Order == nil || *first.Order != 0 {
		t.Fatalf("override 42 = %#v", first)
	}
	second := metadata.Overrides[43]
	if second.Name != "" || second.URL != "" || second.Category != "Infrastructure" || second.Order == nil || *second.Order != maxServiceMetadataOrder {
		t.Fatalf("override 43 = %#v", second)
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
		{name: "wrong version", document: `{"version":6,"services":[]}`, contains: "version"},
		{name: "v5 without bound fields", document: `{"version":5,"services":[]}`, contains: "version"},
		{name: "v1 category", document: `{"version":1,"services":[{"proxy_host_id":1,"category":"secret-name"}]}`, contains: "invalid"},
		{name: "v1 order", document: `{"version":1,"services":[{"proxy_host_id":1,"order":0}]}`, contains: "invalid"},
		{name: "v2 unknown field", document: `{"version":2,"services":[{"proxy_host_id":1,"category":"safe","other":true}]}`, contains: "invalid"},
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
		{name: "empty v2 override", document: `{"version":2,"services":[{"proxy_host_id":1}]}`, contains: "name, url, category, or order"},
		{name: "empty category", document: `{"version":2,"services":[{"proxy_host_id":1,"category":""}]}`, contains: "invalid category"},
		{name: "padded category", document: `{"version":2,"services":[{"proxy_host_id":1,"category":" secret-name "}]}`, contains: "invalid category"},
		{name: "control category", document: "{\"version\":2,\"services\":[{\"proxy_host_id\":1,\"category\":\"secret-name\\nvalue\"}]}", contains: "invalid category"},
		{name: "null category", document: `{"version":2,"services":[{"proxy_host_id":1,"name":"safe","category":null}]}`, contains: "invalid"},
		{name: "null order", document: `{"version":2,"services":[{"proxy_host_id":1,"name":"safe","order":null}]}`, contains: "invalid"},
		{name: "negative order", document: `{"version":2,"services":[{"proxy_host_id":1,"order":-1}]}`, contains: "invalid order"},
		{name: "fractional order", document: `{"version":2,"services":[{"proxy_host_id":1,"order":1.5}]}`, contains: "invalid"},
		{name: "excessive order", document: `{"version":2,"services":[{"proxy_host_id":1,"order":1000001}]}`, contains: "invalid order"},
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

	longCategory := strings.Repeat("x", maxServiceMetadataCategory+1)
	_, err = parseServiceMetadata([]byte(`{"version":2,"services":[{"proxy_host_id":1,"category":"` + longCategory + `"}]}`))
	if err == nil || !strings.Contains(err.Error(), "invalid category") {
		t.Fatalf("long category error = %v", err)
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

func TestConcreteDomainServiceMetadataURLOverrideCount(t *testing.T) {
	order := 4
	metadata := &ServiceMetadata{Overrides: map[int]ServiceOverride{
		1:  {Name: "Intentional", URL: "https://override.example.com/path", Category: "Tools", Order: &order},
		2:  {URL: "https://wildcard.example.com"},
		3:  {Name: "Name only"},
		4:  {Category: "Tools", Order: &order},
		5:  {URL: "https://missing.example.com"},
		0:  {URL: "https://zero.example.com"},
		-1: {URL: "https://negative.example.com"},
	}}
	host := ProxyHost{ID: 1, DomainNames: []string{"*.example.com", " concrete.example.com "}, ForwardScheme: "http"}
	tests := []struct {
		name     string
		metadata *ServiceMetadata
		hosts    []ProxyHost
		want     int
	}{
		{name: "nil metadata", hosts: []ProxyHost{host}},
		{name: "empty metadata", metadata: emptyServiceMetadata(), hosts: []ProxyHost{host}},
		{name: "concrete domain", metadata: metadata, hosts: []ProxyHost{host}, want: 1},
		{name: "wildcard only", metadata: metadata, hosts: []ProxyHost{{ID: 2, DomainNames: []string{"*.example.com"}}}},
		{name: "invalid domain", metadata: metadata, hosts: []ProxyHost{{ID: 1, DomainNames: []string{"https://invalid.example.com", "bad domain"}}}},
		{name: "name category order only", metadata: metadata, hosts: []ProxyHost{{ID: 3, DomainNames: []string{"name.example.com"}}, {ID: 4, DomainNames: []string{"category.example.com"}}}},
		{name: "missing and nonpositive IDs", metadata: metadata, hosts: []ProxyHost{{ID: 0, DomainNames: []string{"zero.example.com"}}, {ID: -1, DomainNames: []string{"negative.example.com"}}, {ID: 6, DomainNames: []string{"absent.example.com"}}}},
		{name: "duplicate ID counted once", metadata: metadata, hosts: []ProxyHost{host, host}, want: 1},
		{name: "later duplicate has concrete domain", metadata: metadata, hosts: []ProxyHost{{ID: 1, DomainNames: []string{"*.example.com"}}, host}, want: 1},
	}
	before, ok := resolveServiceCard(host, metadata)
	if !ok {
		t.Fatal("intentional override card missing")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := concreteDomainServiceMetadataURLOverrideCount(test.metadata, test.hosts); got != test.want {
				t.Fatalf("count = %d, want %d", got, test.want)
			}
		})
	}
	after, ok := resolveServiceCard(host, metadata)
	if !ok || after.URL != before.URL || after.URL != "https://override.example.com/path" || after.Name != "Intentional" || after.Category != "Tools" || after.Order == nil || *after.Order != order {
		t.Fatalf("count changed intentional override: before=%#v after=%#v", before, after)
	}
}

func TestUnmatchedServiceMetadataCount(t *testing.T) {
	metadata := &ServiceMetadata{Overrides: map[int]ServiceOverride{1: {}, 2: {}, 3: {}}}
	if got := unmatchedServiceMetadataCount(metadata, []ProxyHost{{ID: 1}, {ID: 3}}); got != 1 {
		t.Fatalf("unmatchedServiceMetadataCount() = %d", got)
	}
}

func TestServiceMetadataBinding(t *testing.T) {
	original, err := parseServiceMetadata([]byte(metadataV4Fixture))
	if err != nil {
		t.Fatal(err)
	}
	binding := serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "portal-demo-01"}
	for _, tc := range []string{"equal", "missing-runtime", "missing-namespace", "missing-provider", "wrong-namespace", "wrong-provider", "invalid-namespace", "invalid-provider", "missing-document-namespace", "missing-document-provider", "headscale-empty", "headscale-machines", "headscale-applications", "provider-switch"} {
		t.Run(tc, func(t *testing.T) {
			doc := cloneServiceMetadataDocument(original.document)
			b := binding
			switch tc {
			case "missing-runtime":
				b = serviceMetadataBinding{}
			case "missing-namespace":
				b.Namespace = ""
			case "missing-provider":
				b.ControlPlane = ""
			case "wrong-namespace":
				b.Namespace = "namespace-canary"
			case "wrong-provider":
				b.ControlPlane = controlPlaneHeadscale
			case "invalid-namespace":
				b.Namespace = " Invalid-Canary "
			case "invalid-provider":
				b.ControlPlane = "provider-canary"
			case "missing-document-namespace":
				doc.Namespace = ""
			case "missing-document-provider":
				doc.ControlPlane = ""
			case "headscale-empty", "headscale-machines", "headscale-applications", "provider-switch":
				doc.ControlPlane = controlPlaneHeadscale
				b.ControlPlane = controlPlaneHeadscale
				if tc != "headscale-machines" {
					doc.Machines = []machineIconEntry{}
				}
				if tc != "headscale-applications" {
					doc.Applications = []applicationIconEntry{}
				}
				if tc == "provider-switch" {
					b.ControlPlane = controlPlaneTailscale
				}
			}
			before := cloneServiceMetadataDocument(doc)
			err := validateServiceMetadataBinding(doc, b)
			good := tc == "equal" || tc == "headscale-empty"
			if (err == nil) != good {
				t.Fatalf("binding result %v", err)
			}
			if !reflect.DeepEqual(before, doc) || len(original.MachineIcons) != 2 || len(original.ApplicationIcons) != 2 {
				t.Fatal("binding validator mutated/dropped data")
			}
			if err != nil {
				for _, canary := range []string{"portal-demo-01", "namespace-canary", "Invalid-Canary", "provider-canary", "sha256:", "same"} {
					if strings.Contains(err.Error(), canary) {
						t.Fatal("binding error leaked document/runtime value")
					}
				}
			}
		})
	}
	for _, version := range []int{1, 2, 3} {
		m, err := parseBoundRuntimeServiceMetadata([]byte(`{"version":`+strconv.Itoa(version)+`,"services":[]}`), serviceMetadataBinding{})
		if err != nil || m.document.Version != version {
			t.Fatal("legacy zero binding rejected", err)
		}
	}
	if len(original.Overrides) != 3 || unmatchedServiceMetadataCount(original, []ProxyHost{{ID: 3}}) != 2 {
		t.Fatal("counts no longer NPM-only")
	}
}

func TestServiceMetadataBoundRuntimeGate(t *testing.T) {
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
	path := editorTestFile(t, metadataV4Fixture)
	before, _ := os.ReadFile(path)
	for _, b := range []serviceMetadataBinding{
		{}, {ControlPlane: controlPlaneTailscale, Namespace: "portal-demo-01"},
		{ControlPlane: controlPlaneTailscale, Namespace: "different-canary"},
		{ControlPlane: controlPlaneHeadscale, Namespace: "portal-demo-01"},
	} {
		good := b == (serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "portal-demo-01"})
		for name, read := range map[string]func() (*ServiceMetadata, error){
			"bound-file":             func() (*ServiceMetadata, error) { return loadBoundServiceMetadataFile(path, b) },
			"bound-loader":           boundServiceMetadataLoaderForPath(path, b),
			"bound-parser":           func() (*ServiceMetadata, error) { return parseBoundRuntimeServiceMetadata(before, b) },
			"missing-legacy-binding": serviceMetadataLoaderForPath(path),
		} {
			m, err := read()
			want := good && name != "missing-legacy-binding"
			if (err == nil) != want || (m != nil) != want {
				t.Fatalf("%s binding result: %v", name, err)
			}
			if err != nil && (strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "portal-demo-01")) {
				t.Fatal("reader leaked binding/path")
			}
		}
		if (inspectBoundServiceMetadataEditorStorage(path, b) == nil) != good {
			t.Fatal("diagnostic binding result")
		}
		cfg := editorTestConfig()
		cfg.Binding = b
		e, err := newServiceMetadataEditor(path, cfg)
		if (err == nil) != good || (e != nil) != good {
			t.Fatal("editor binding result", err)
		}
		if e != nil {
			_ = e.Close()
		}
		if (preflightServiceMetadataBinding(path, b) == nil) != good {
			t.Fatal("startup preflight binding result")
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("reader rewrote configured v4")
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("successful configured writer lacks lifetime lock", err)
	}

	// Exercise held-directory bytes through the same bound parser used by both
	// constructor reads, without bypassing the production constructor gate.
	e, _, _ := editorFixture(t)
	defer e.Close()
	if err := os.WriteFile(e.path, before, 0600); err != nil {
		t.Fatal(err)
	}
	raw, f, err := e.readFile()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if m, err := parseBoundRuntimeServiceMetadata(raw, e.config.Binding); err == nil || m != nil {
		t.Fatal("locked reread accepted v4")
	}
}

func TestServiceMetadataBoundStartupModes(t *testing.T) {
	for _, source := range []struct{ name, provider, document, namespace string }{
		{"tailscale-equal", "tailscale", metadataV4Fixture, "portal-demo-01"},
		{"tailscale-missing", "tailscale", metadataV4Fixture, ""},
		{"tailscale-mismatch", "tailscale", metadataV4Fixture, "wrong-canary"},
		{"provider-switch", "headscale", metadataV4Fixture, "portal-demo-01"},
		{"headscale-empty", "headscale", `{"version":4,"namespace":"portal-demo-01","control_plane":"headscale","services":[],"machines":[],"applications":[]}`, "portal-demo-01"},
	} {
		for _, mode := range []string{"off", "browser", "shared"} {
			t.Run(source.name+"/"+mode, func(t *testing.T) {
				path := editorTestFile(t, source.document)
				values := namespaceConfigValues(source.provider, mode)
				values["SERVICE_METADATA_FILE"] = path
				if source.namespace != "" {
					values["PORTAL_STATE_NAMESPACE"] = source.namespace
				}
				cfg, err := loadConfigFrom(mapConfigLookup(values))
				if err != nil {
					t.Fatal("syntax-only config read document", err)
				}
				e, p, err := newPresentationStores(cfg)
				good := source.name == "tailscale-equal" || source.name == "headscale-empty"
				if (err == nil) != good || (e != nil) != (good && mode == "shared") || p != nil {
					t.Fatal("configured binding/mode result", err)
				}
				defer e.Close()
				if _, statErr := os.Stat(path + ".lock"); (statErr == nil) != (good && mode == "shared") {
					t.Fatal("unexpected writer lock", statErr)
				}
				after, _ := os.ReadFile(path)
				if string(after) != source.document {
					t.Fatal("startup rewrote v4")
				}
			})
		}
	}
	for _, version := range []int{1, 2, 3} {
		for _, mode := range []string{"off", "browser", "shared"} {
			t.Run("legacy-"+strconv.Itoa(version)+"/"+mode, func(t *testing.T) {
				source := " {\n\"version\":" + strconv.Itoa(version) + ", \"services\":[] } \n"
				path := editorTestFile(t, source)
				values := namespaceConfigValues("headscale", mode)
				values["SERVICE_METADATA_FILE"] = path
				values["PORTAL_STATE_NAMESPACE"] = "legacy-ns"
				cfg, err := loadConfigFrom(mapConfigLookup(values))
				if err != nil {
					t.Fatal(err)
				}
				e, p, err := newPresentationStores(cfg)
				if err != nil || p != nil || (e != nil) != (mode == "shared") {
					t.Fatal("legacy startup changed", err)
				}
				defer e.Close()
				if e != nil && e.config.Binding != cfg.metadataBinding() {
					t.Fatal("lifecycle lost common binding")
				}
				m, err := boundServiceMetadataLoaderForPath(path, cfg.metadataBinding())()
				if err != nil || m.document.Version != version {
					t.Fatal("legacy bound read changed", err)
				}
				after, _ := os.ReadFile(path)
				if string(after) != source {
					t.Fatal("startup canonicalized legacy bytes")
				}
			})
		}
	}
	// Readonly legacy read errors retain the established cold/poll behavior;
	// shared mode still rejects unsafe/missing input through its own constructor.
	for _, source := range []string{"missing", "invalid"} {
		path := filepath.Join(t.TempDir(), "legacy.json")
		if source == "invalid" {
			if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cfg := &Config{ControlPlane: controlPlaneHeadscale, ServiceMetadataFile: path, PortalEditMode: serviceEditOff}
		e, p, err := newPresentationStores(cfg)
		if err != nil || e != nil || p != nil {
			t.Fatal("legacy readonly startup behavior changed", err)
		}
		if m, err := boundServiceMetadataLoaderForPath(path, cfg.metadataBinding())(); err == nil || m != nil {
			t.Fatal("invalid legacy refresh accepted")
		}
	}
}

func TestServiceMetadataPreflightExactVersionCompatibility(t *testing.T) {
	for _, mode := range []string{"off", "browser"} {
		for _, source := range []string{
			`{"version":3,"services":[],"Version":4}`,
			`{"Version":4,"services":[],"version":3}`,
			`{"version":2,"services":[],"VERSION":4}`,
			`{"Version":4,"services":[]}`,
			`{"version":1,"services":[],"vErSiOn":4}`,
		} {
			t.Run(mode+"/"+source, func(t *testing.T) {
				path := editorTestFile(t, source)
				values := namespaceConfigValues("headscale", mode)
				values["SERVICE_METADATA_FILE"] = path
				cfg, err := loadConfigFrom(mapConfigLookup(values))
				if err != nil {
					t.Fatal(err)
				}
				e, p, err := newPresentationStores(cfg)
				if e != nil {
					defer e.Close()
				}
				if p != nil {
					defer p.Close()
				}
				if err != nil || e != nil || p != nil {
					t.Fatal("invalid legacy file unexpectedly stopped readonly startup", err)
				}
				load := boundServiceMetadataLoaderForPath(path, cfg.metadataBinding())
				if m, err := load(); err == nil || m != nil {
					t.Fatal("strict refresh accepted case-variant field")
				}
				after, _ := os.ReadFile(path)
				if string(after) != source {
					t.Fatal("preflight rewrote input")
				}
				if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("readonly preflight created a lock")
				}
				// The same loader can recover after correction without restarting.
				if err := os.WriteFile(path, []byte(`{"version":3,"services":[]}`), 0600); err != nil {
					t.Fatal(err)
				}
				if m, err := load(); err != nil || m == nil || m.document.Version != 3 {
					t.Fatal("corrected legacy refresh did not recover", err)
				}
			})
		}
		for _, source := range []string{
			`{"version":4,"services":[],"Version":3}`,
			`{"Version":3,"version":4,"services":[]}`,
			`{"version":4,"services":[],"VERSION":1}`,
		} {
			t.Run(mode+"/canonical-v4/"+source, func(t *testing.T) {
				path := editorTestFile(t, source)
				values := namespaceConfigValues("headscale", mode)
				values["SERVICE_METADATA_FILE"] = path
				cfg, err := loadConfigFrom(mapConfigLookup(values))
				if err != nil {
					t.Fatal(err)
				}
				e, p, err := newPresentationStores(cfg)
				if e != nil {
					defer e.Close()
				}
				if p != nil {
					defer p.Close()
				}
				if err == nil || e != nil || p != nil {
					t.Fatal("case alias bypassed canonical v4 strict preflight")
				}
			})
		}
	}
}

func TestServiceMetadataBoundCommandPaths(t *testing.T) {
	for _, namespace := range []string{"", "portal-demo-01", "wrong-canary"} {
		t.Run("namespace-"+namespace, func(t *testing.T) {
			fixture := newDoctorHTTPFixture(t)
			path := editorTestFile(t, metadataV4Fixture)
			values := doctorFixtureConfig(fixture)
			values["SERVICE_METADATA_FILE"] = path
			values["CONTROL_PLANE"] = "headscale"
			if namespace != "" {
				values["PORTAL_STATE_NAMESPACE"] = namespace
			}
			env := filepath.Join(t.TempDir(), "fixture.env")
			if err := writeEnvFile(env, values); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runDoctorForTest([]string{"--env-file", env}, fixture)
			if code != 1 || !strings.Contains(out, "FAIL service metadata:") {
				t.Fatalf("Doctor accepted v4: %d %s %s", code, out, stderr)
			}
			if fixture.policyHits.Load() != 0 || fixture.nodesHits.Load() != 0 || fixture.authHits.Load() != 0 || fixture.proxyHits.Load() != 0 {
				t.Fatal("Doctor contacted upstream after v4 rejection")
			}
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if err != nil {
				t.Fatal(err)
			}
			data, names, err := loadHostnameSuggestionData(context.Background(), cfg)
			if err == nil || data != nil || names != nil {
				t.Fatal("proposal loader accepted partial v4")
			}
			deps := validationDependenciesFor(validationTestSnapshot())
			deps.loadSnapshot = func(context.Context, ControlPlane, *NPMClient) (*CacheData, error) {
				t.Fatal("validation contacted upstream after v4 rejection")
				return nil, nil
			}
			vcode, vout, verr := runValidationForTest([]string{"--env-file", env, "--identity", "alpha=alice@example.com", "--identity", "beta=bob@example.com", "--format", "json"}, deps)
			if vcode != 1 || vout != "" || !strings.Contains(verr, "service metadata") {
				t.Fatalf("validation accepted v4: %d %s %s", vcode, vout, verr)
			}
			for _, secret := range []string{path, "portal-demo-01", "wrong-canary", "same", "sha256:"} {
				if strings.Contains(out+stderr+vout+verr+err.Error(), secret) {
					t.Fatal("command disclosed metadata binding/target/path")
				}
			}
			if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("readonly command created lock")
			}
			after, _ := os.ReadFile(path)
			if string(after) != metadataV4Fixture {
				t.Fatal("command rewrote active metadata")
			}
		})
	}
}
