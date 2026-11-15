package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func personalApplicationFixture(t *testing.T) (*Identity, *CacheData, PersonalServicesState, time.Time, serviceMetadataBinding) {
	t.Helper()
	identity, data, state, now := personalProjectionFixture(t)
	data.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{data.Nodes[0].ID: {controlPlaneTailscale, deviceIDKindID, "device-1"}}
	state.Document.Entries[0].URL = "https://server.tailnet.ts.net/photos"
	state.Document.Entries[0].Icon = ""
	return identity, data, state, now, serviceMetadataBinding{controlPlaneTailscale, "portal-demo-01"}
}

func evaluatedApplicationDigest(t *testing.T, identity *Identity, data *CacheData, entry PersonalService, now time.Time, binding serviceMetadataBinding) string {
	t.Helper()
	evidence, active := newPersonalServiceEvaluator(identity, data, now, 30*time.Second).evaluate(entry)
	if !active {
		t.Fatal("test destination is not active")
	}
	digest, ok := personalApplicationDigest(binding, data, evidence)
	if !ok {
		t.Fatal("test destination lacks stable evidence")
	}
	return digest
}

func TestPersonalApplicationDigestFrozenVectorAndFraming(t *testing.T) {
	identity, data, state, now, binding := personalApplicationFixture(t)
	got := evaluatedApplicationDigest(t, identity, data, state.Document.Entries[0], now, binding)
	const want = "sha256:5cc8abe9c205a36d8442a36a51d41c22eb0e90f9623416ed1f3b2269030dfe5c"
	if got != want {
		t.Fatalf("frozen digest = %s, want %s", got, want)
	}
	// Build the specified wire preimage separately, with an explicit uint32 BE
	// frame for each of exactly eight fields and an ASCII-domain NUL terminator.
	fields := []string{"portal-demo-01", "tailscale", "personal_application", "id", "device-1", "https", "443", "/photos"}
	encode := func(fields []string, domain string, order binary.ByteOrder) string {
		var raw bytes.Buffer
		raw.WriteString(domain)
		for _, field := range fields {
			if err := binary.Write(&raw, order, uint32(len([]byte(field)))); err != nil {
				t.Fatal(err)
			}
			raw.WriteString(field)
		}
		sum := sha256.Sum256(raw.Bytes())
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	if encode(fields, "velociportal/shared-application/v1\x00", binary.BigEndian) != got {
		t.Fatal("eight-field preimage mismatch")
	}
	for i := range fields {
		changed := append([]string(nil), fields...)
		changed[i] += "x"
		if encode(changed, "velociportal/shared-application/v1\x00", binary.BigEndian) == got {
			t.Fatal("field omitted from digest", i)
		}
	}
	for _, other := range []string{
		encode(fields, "velociportal/shared-application/v1", binary.BigEndian),
		encode(fields, "velociportal/shared-machine/v1\x00", binary.BigEndian),
		encode(fields, "velociportal/shared-application/v1\x00", binary.LittleEndian),
		encode(append(fields, ""), "velociportal/shared-application/v1\x00", binary.BigEndian),
	} {
		if other == got {
			t.Fatal("domain, framing or field count not separated")
		}
	}
}

func TestPersonalApplicationDigestCanonicalPathPortBoundaries(t *testing.T) {
	seen := map[string]string{}
	for _, tt := range []struct {
		name, suffix, path string
		port               int
		equivalent         string
	}{
		{"empty", "", "/", 443, "root"}, {"root", "/", "/", 443, "root"},
		{"app", "/app", "/app", 443, "app"}, {"trailing slash", "/app/", "/app/", 443, "trailing"},
		{"case", "/App", "/App", 443, "case"}, {"unicode composed", "/%C3%A9", "/%C3%A9", 443, "composed"},
		{"unicode decomposed", "/e%CC%81", "/e%CC%81", 443, "decomposed"}, {"snowman", "/%E2%98%83", "/%E2%98%83", 443, "snowman"},
		{"min port", ":1/app", "/app", 1, "min"}, {"max port", ":65535/app", "/app", 65535, "max"},
		{"nondefault port", ":8443/app", "/app", 8443, "nondefault"},
		// These adjacent device/port strings would collide if delimiter-free
		// concatenation replaced the required field lengths (see next test).
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity, data, state, now, binding := personalApplicationFixture(t)
			entry := state.Document.Entries[0]
			entry.URL = "https://server.tailnet.ts.net" + tt.suffix
			data.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:*")}
			evidence, active := newPersonalServiceEvaluator(identity, data, now, 30*time.Second).evaluate(entry)
			if !active || evidence.port != tt.port || evidence.escapedPath != tt.path {
				t.Fatalf("noncanonical evidence %#v active=%v", evidence, active)
			}
			digest, ok := personalApplicationDigest(binding, data, evidence)
			if !ok {
				t.Fatal("no digest")
			}
			for key, prior := range seen {
				if (prior == digest) != (key == tt.equivalent) {
					t.Fatal("path or port collapsed", key, tt.name)
				}
			}
			seen[tt.equivalent] = digest
		})
	}
	for _, suffix := range []string{":443", ":443/", ":0443/app", ":0/app", ":65536/app", ":/app", "/./app", "/../app", "//app", "/%2Fapp", "/%61pp", "/%c3%a9", "/☃", "/%FF", "/%00", "/?x=1", "/#x", "/app?", "/app#"} {
		t.Run("reject "+suffix, func(t *testing.T) {
			identity, data, state, now, binding := personalApplicationFixture(t)
			entry := state.Document.Entries[0]
			entry.URL = "https://server.tailnet.ts.net" + suffix
			evidence, active := newPersonalServiceEvaluator(identity, data, now, 30*time.Second).evaluate(entry)
			if active || !reflect.DeepEqual(evidence, personalServiceEvidence{}) {
				t.Fatal("digest work broadened URL acceptance")
			}
			if digest, ok := personalApplicationDigest(binding, data, evidence); ok || digest != "" {
				t.Fatal("inactive input acquired alternate digest")
			}
		})
	}
}

func TestPersonalApplicationDigestTupleSeparation(t *testing.T) {
	identity, data, state, now, binding := personalApplicationFixture(t)
	entry := state.Document.Entries[0]
	original := evaluatedApplicationDigest(t, identity, data, entry, now, binding)
	otherBinding := binding
	otherBinding.Namespace = "portal-demo-02"
	if evaluatedApplicationDigest(t, identity, data, entry, now, otherBinding) == original {
		t.Fatal("namespace omitted")
	}
	for _, id := range []ProviderDeviceID{{controlPlaneTailscale, deviceIDKindNodeID, "device-1"}, {controlPlaneTailscale, deviceIDKindID, "device-2"}, {controlPlaneTailscale, deviceIDKindID, "Device-1"}, {controlPlaneTailscale, deviceIDKindID, "042"}} {
		data.PresentationDeviceIDsByNodeID[data.Nodes[0].ID] = id
		if evaluatedApplicationDigest(t, identity, data, entry, now, binding) == original {
			t.Fatal("typed identity collapsed")
		}
	}
	// Same delimiter-only join with a legal delimiter inside one opaque field:
	// namespace a_b + id c versus namespace a + id b_c must remain distinct.
	binding.Namespace = "a_b"
	data.PresentationDeviceIDsByNodeID[data.Nodes[0].ID] = ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "c"}
	a := evaluatedApplicationDigest(t, identity, data, entry, now, binding)
	binding.Namespace = "a"
	data.PresentationDeviceIDsByNodeID[data.Nodes[0].ID] = ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "b_c"}
	if evaluatedApplicationDigest(t, identity, data, entry, now, binding) == a {
		t.Fatal("tuple boundaries collapsed")
	}
}

func TestPersonalApplicationDigestUnavailableEvidence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*CacheData, *serviceMetadataBinding, *personalServiceEvidence)
	}{
		{"no namespace", func(_ *CacheData, b *serviceMetadataBinding, _ *personalServiceEvidence) { b.Namespace = "" }},
		{"bad namespace", func(_ *CacheData, b *serviceMetadataBinding, _ *personalServiceEvidence) { b.Namespace = " Demo " }},
		{"Headscale binding", func(_ *CacheData, b *serviceMetadataBinding, _ *personalServiceEvidence) {
			b.ControlPlane = controlPlaneHeadscale
		}},
		{"Headscale snapshot", func(d *CacheData, _ *serviceMetadataBinding, _ *personalServiceEvidence) {
			d.ControlPlane.Provider = controlPlaneHeadscale
		}},
		{"absent map", func(d *CacheData, _ *serviceMetadataBinding, _ *personalServiceEvidence) {
			d.PresentationDeviceIDsByNodeID = nil
		}},
		{"wrong node key", func(d *CacheData, _ *serviceMetadataBinding, _ *personalServiceEvidence) {
			d.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{"other": {controlPlaneTailscale, deviceIDKindID, "device-1"}}
		}},
		{"wrong ID provider", func(d *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) {
			d.PresentationDeviceIDsByNodeID[e.node.ID] = ProviderDeviceID{controlPlaneHeadscale, deviceIDKindID, "device-1"}
		}},
		{"wrong kind", func(d *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) {
			d.PresentationDeviceIDsByNodeID[e.node.ID] = ProviderDeviceID{controlPlaneTailscale, "ID", "device-1"}
		}},
		{"invalid value", func(d *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) {
			d.PresentationDeviceIDsByNodeID[e.node.ID] = ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, " device-1 "}
		}},
		{"no node", func(_ *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) { e.node = Node{} }},
		{"no port", func(_ *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) { e.port = 0 }},
		{"port overflow", func(_ *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) { e.port = 65536 }},
		{"no path", func(_ *CacheData, _ *serviceMetadataBinding, e *personalServiceEvidence) { e.escapedPath = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity, data, state, now, binding := personalApplicationFixture(t)
			evidence, active := newPersonalServiceEvaluator(identity, data, now, 30*time.Second).evaluate(state.Document.Entries[0])
			if !active {
				t.Fatal("fixture inactive")
			}
			tt.change(data, &binding, &evidence)
			if digest, ok := personalApplicationDigest(binding, data, evidence); ok || digest != "" {
				t.Fatal("missing evidence received alternate identity")
			}
		})
	}
	if digest, ok := personalApplicationDigest(serviceMetadataBinding{}, nil, personalServiceEvidence{}); ok || digest != "" {
		t.Fatal("nil evidence")
	}
}

func TestPersonalApplicationDigestFreshEvaluatorRequirements(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		interval             time.Duration
		nilIdentity, nilData bool
	}{
		{"zero interval", 0, false, false}, {"negative interval", -time.Second, false, false}, {"nil identity", time.Second, true, false}, {"nil snapshot", time.Second, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity, data, state, now, binding := personalApplicationFixture(t)
			if tt.nilIdentity {
				identity = nil
			}
			if tt.nilData {
				data = nil
			}
			evidence, active := newPersonalServiceEvaluator(identity, data, now, tt.interval).evaluate(state.Document.Entries[0])
			if active {
				t.Fatal("inactive evaluator returned evidence")
			}
			if digest, ok := personalApplicationDigest(binding, data, evidence); ok || digest != "" {
				t.Fatal("inactive evaluator acquired digest")
			}
		})
	}
	// The incumbent full membership/Grant/unique-host/freshness matrix is also
	// asserted against this evaluator in personal_services_test.go.
}

func TestPersonalApplicationDigestRenameReplacementAtAPI(t *testing.T) {
	u := newTestUpstreams(t)
	binding := serviceMetadataBinding{controlPlaneTailscale, "portal-demo-01"}
	var first string
	for _, tt := range []struct {
		name, fields, hostname string
		same                   bool
	}{
		{"initial", `"id":"device-1"`, "server.tailnet.ts.net", true},
		{"rename", `"id":"device-1"`, "renamed.tailnet.ts.net", true},
		{"replacement", `"id":"device-2"`, "server.tailnet.ts.net", false},
		{"kind transition", `"nodeId":"device-1"`, "server.tailnet.ts.net", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := strings.Replace(presentationTestRecord(tt.fields), "server.tailnet.ts.net", tt.hostname, 1)
			client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+record+`]}`, presentationTestPolicy)
			defer closeServer()
			data, err := loadSnapshot(context.Background(), client, u.npm)
			if err != nil {
				t.Fatal(err)
			}
			entry := personalTestEntry(0)
			entry.URL = "https://" + tt.hostname + "/photos"
			digest := evaluatedApplicationDigest(t, &Identity{Login: "alice@example.com"}, data, entry, data.UpdatedAt, binding)
			if first == "" {
				first = digest
			}
			if (digest == first) != tt.same {
				t.Fatal("rename/replacement identity contract changed")
			}
			if tt.name == "rename" {
				entry.URL = "https://server.tailnet.ts.net/photos"
				if _, active := newPersonalServiceEvaluator(&Identity{Login: "alice@example.com"}, data, data.UpdatedAt, time.Minute).evaluate(entry); active {
					t.Fatal("old hostname remained active")
				}
			}
		})
	}
}

func TestPersonalApplicationEvidenceNeverChangesAuthoredRecords(t *testing.T) {
	identity, data, state, now, binding := personalApplicationFixture(t)
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	original := evaluatedApplicationDigest(t, identity, data, state.Document.Entries[0], now, binding)
	evidence, active := newPersonalServiceEvaluator(identity, data, now, 30*time.Second).evaluate(state.Document.Entries[0])
	if !active {
		t.Fatal("fixture inactive")
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("public evidence fields", string(encoded), err)
	}
	cards, statuses := ProjectPersonalServices(identity, data, state, now, 30*time.Second)
	after, _ := json.Marshal(state)
	if !bytes.Equal(before, after) || len(cards) != 1 || cards[0].Icon != "" || statuses[0].Status != "active" {
		t.Fatal("authored or rendering state changed")
	}
	for _, value := range []any{state, state.Document, cards, statuses} {
		wire, _ := json.Marshal(value)
		for _, forbidden := range []string{"sha256:", "device-1", "escapedPath", "PresentationDeviceIDsByNodeID"} {
			if strings.Contains(string(wire), forbidden) {
				t.Fatal("evidence leaked", forbidden)
			}
		}
	}
	// Different owners/records/presentation still address the same destination.
	identity.Login = "bob@example.com"
	data.GrantRoleSelectorsByLogin = map[string][]string{identity.Login: {"autogroup:member"}}
	data.Policy.Grants[0].BrowserSrc = []string{identity.Login}
	data.Policy.Grants[0].Src = []string{identity.Login}
	data.Nodes[0].OwnerLogin = identity.Login
	data.Nodes[0].Name = "renamed.tailnet.ts.net"
	data.Nodes[0].Addresses = []string{"100.64.0.20"}
	entry := personalTestEntry(9)
	entry.Name = "Different name"
	entry.Icon = "generic"
	entry.URL = "https://renamed.tailnet.ts.net/photos"
	if evaluatedApplicationDigest(t, identity, data, entry, now, binding) != original {
		t.Fatal("hostname/address/login/owner/personal ID/name/icon entered digest")
	}
	// No call above may materialize a derived value in a private collection.
	store := personalTestStore(t)
	stored, _ := store.Read(identity.Login)
	mutation := personalTestMutation(stored, "create")
	mutation.URL = entry.URL
	mutation.Icon = ""
	stored, err = store.Mutate(identity.Login, mutation)
	if err != nil {
		t.Fatal(err)
	}
	storedBefore, _ := json.Marshal(stored)
	_ = evaluatedApplicationDigest(t, identity, data, stored.Document.Entries[0], now, binding)
	ProjectPersonalServices(identity, data, stored, now, 30*time.Second)
	storedAfter, err := store.Read(identity.Login)
	if err != nil {
		t.Fatal(err)
	}
	storedWire, _ := json.Marshal(storedAfter)
	if !bytes.Equal(storedBefore, storedWire) || storedAfter.Document.Entries[0].Icon != "" {
		t.Fatal("private revision/authored record changed")
	}
}
