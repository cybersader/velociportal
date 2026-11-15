package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const presentationTestUsers = `{"users":[{"id":"user-1","loginName":"alice@example.com","type":"member","role":"member"}]}`
const presentationTestPolicy = `{"grants":[{"src":["alice@example.com"],"dst":["tag:server"],"ip":["tcp:*"]}],"ssh":[{"action":"accept","src":["alice@example.com"],"dst":["tag:server"],"users":["deploy"]}]}`

func presentationTestRecord(fields string) string {
	return `{` + fields + `,"name":"server.tailnet.ts.net","hostname":"server","user":"user-1","tags":["tag:server"],"addresses":["100.64.0.10"],"sshEnabled":true,"blocksIncomingConnections":false}`
}

func TestTailscalePresentationIdentityAtAPIIngestion(t *testing.T) {
	u := newTestUpstreams(t)
	for _, tt := range []struct {
		name, fields, legacy, value string
		kind                        deviceIDKind
	}{
		{"string", `"id":"Ab9_-"`, "Ab9_-", "Ab9_-", deviceIDKindID},
		{"numeric", `"id":42`, "42", "42", deviceIDKindID},
		{"zero", `"id":0`, "0", "0", deviceIDKindID},
		{"numeric string", `"id":"42"`, "42", "42", deviceIDKindID},
		{"opaque leading zero", `"id":"042"`, "042", "042", deviceIDKindID},
		{"escaped ASCII", `"id":"device-1"`, "device-1", "device-1", deviceIDKindID},
		{"absent fallback", `"nodeId":"node-1"`, "node-1", "node-1", deviceIDKindNodeID},
		{"null fallback", `"id":null,"nodeId":"node-1"`, "node-1", "node-1", deviceIDKindNodeID},
		{"exact empty fallback", `"id":"","nodeId":"node-1"`, "node-1", "node-1", deviceIDKindNodeID},
		{"both id wins", `"id":"device-1","nodeId":"node-1"`, "device-1", "device-1", deviceIDKindID},
		{"numeric id wins", `"id":42,"nodeId":"node-1"`, "42", "42", deviceIDKindID},
		{"id wins over padded alternative", `"id":"device-1","nodeId":" padded "`, "device-1", "device-1", deviceIDKindID},
		{"string maximum", `"id":"` + strings.Repeat("a", 128) + `"`, strings.Repeat("a", 128), strings.Repeat("a", 128), deviceIDKindID},
		{"numeric maximum", `"id":` + strings.Repeat("9", 128), strings.Repeat("9", 128), strings.Repeat("9", 128), deviceIDKindID},
		{"fallback maximum", `"nodeId":"` + strings.Repeat("n", 128) + `"`, strings.Repeat("n", 128), strings.Repeat("n", 128), deviceIDKindNodeID},
		{"string too long", `"id":"` + strings.Repeat("a", 129) + `"`, strings.Repeat("a", 129), "", ""},
		{"numeric too long", `"id":` + strings.Repeat("9", 129), strings.Repeat("9", 129), "", ""},
		{"fallback too long", `"nodeId":"` + strings.Repeat("n", 129) + `"`, strings.Repeat("n", 129), "", ""},
		{"padded selected", `"id":" device-1 ","nodeId":"safe"`, "device-1", "", ""},
		{"whitespace no stable fallback", `"id":" \t ","nodeId":"safe"`, "safe", "", ""},
		{"padded fallback", `"nodeId":" node-1 "`, "node-1", "", ""},
		{"underscore first", `"id":"_device","nodeId":"safe"`, "_device", "", ""},
		{"punctuation", `"id":"device.1","nodeId":"safe"`, "device.1", "", ""},
		{"negative", `"id":-42,"nodeId":"safe"`, "-42", "", ""},
		{"negative zero", `"id":-0`, "-0", "", ""},
		{"exponent", `"id":4.2e1,"nodeId":"safe"`, "4.2e1", "", ""},
		{"integer exponent", `"id":42e0`, "42e0", "", ""},
		{"fraction", `"id":42.0`, "42.0", "", ""},
		{"unicode", `"id":"dévice"`, "dévice", "", ""},
		{"replacement literal", `"id":"d�"`, "d�", "", ""},
		{"invalid UTF8", "\"id\":\"d\xff\"", "d�", "", ""},
		{"unpaired surrogate", `"id":"d\ud800"`, "d�", "", ""},
		{"invalid fallback scalar", `"nodeId":"n\udc00"`, "n�", "", ""},
		{"invalid alternative scalar", `"id":"device-1","nodeId":"n\ud800"`, "device-1", "", ""},
		{"control scalar", `"id":"d\u0000x"`, "d\x00x", "", ""},
		{"duplicate id equal", `"id":"device-1","id":"device-1"`, "device-1", "", ""},
		{"duplicate id differs", `"id":"old","id":"device-1"`, "device-1", "", ""},
		{"duplicate to fallback", `"id":"old","id":null,"nodeId":"safe"`, "safe", "", ""},
		{"duplicate nodeId", `"id":"device-1","nodeId":"a","nodeId":"b"`, "device-1", "", ""},
		{"alias ID only", `"ID":"device-1"`, "device-1", "", ""},
		{"alias id overrides", `"id":"old","ID":"device-1"`, "device-1", "", ""},
		{"alias nodeId only", `"NodeId":"node-1"`, "node-1", "", ""},
		{"alias alternative", `"id":"device-1","NODEID":"node-1"`, "device-1", "", ""},
		{"unknown ordinary fields", `"id":"device-1","profile":{"id":"nested","ID":false},"profile":7,"custom":"\ud800"`, "device-1", "device-1", deviceIDKindID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+presentationTestRecord(tt.fields)+`]}`, presentationTestPolicy)
			defer closeServer()
			data, err := loadSnapshot(context.Background(), client, u.npm)
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Nodes) != 1 || data.Nodes[0].ID != tt.legacy || !data.MachineSSHCapableByID[tt.legacy] {
				t.Fatalf("legacy device/capability changed: %#v", data)
			}
			if tt.value == "" {
				if len(data.PresentationDeviceIDsByNodeID) != 0 {
					t.Fatal("unsupported identity acquired stable evidence")
				}
			} else {
				want := map[string]ProviderDeviceID{tt.legacy: {controlPlaneTailscale, tt.kind, tt.value}}
				if !reflect.DeepEqual(data.PresentationDeviceIDsByNodeID, want) {
					t.Fatalf("typed IDs = %#v, want %#v", data.PresentationDeviceIDsByNodeID, want)
				}
			}
			// Supported cards remain identical even when stable evidence is absent.
			data.ProxyHosts = []ProxyHost{{ID: 1, Enabled: true, DomainNames: []string{"app.example.com"}, ForwardHost: "100.64.0.10", ForwardPort: 443, ForwardScheme: "https"}}
			identity := &Identity{Login: "alice@example.com"}
			services := MatchServices(identity, data)
			machines := MatchMachines(identity, data)
			state := PersonalServicesState{Document: PersonalServicesDocument{Version: 1, Entries: []PersonalService{personalTestEntry(0)}}}
			cards, statuses := ProjectPersonalServices(identity, data, state, data.UpdatedAt, 30*time.Second)
			if len(services) != 1 || len(machines) != 1 || len(cards) != 1 || statuses[0].Status != "active" {
				t.Fatal("stable suppression changed ordinary visibility")
			}
			without := *data
			without.PresentationDeviceIDsByNodeID = nil
			cardsWithout, statusesWithout := ProjectPersonalServices(identity, &without, state, data.UpdatedAt, 30*time.Second)
			if !reflect.DeepEqual(services, MatchServices(identity, &without)) || !reflect.DeepEqual(machines, MatchMachines(identity, &without)) || !reflect.DeepEqual(cards, cardsWithout) || !reflect.DeepEqual(statuses, statusesWithout) {
				t.Fatal("presentation IDs became visibility inputs")
			}
		})
	}
}

func TestTailscalePresentationTopLevelAlignment(t *testing.T) {
	record := presentationTestRecord(`"id":"device-1"`)
	for _, body := range []string{
		`{"devices":[],"devices":[` + record + `]}`,
		`{"devices":[` + record + `],"Devices":[]}`,
		`{"DEVICES":[],"devices":[` + record + `]}`,
		`{"devices":[` + record + `],"deviceS":[` + record + `]}`,
		`{"devices":[` + record + `],"deviceſ":[]}`,
	} {
		client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, body, presentationTestPolicy)
		result, err := client.Load(context.Background(), nil)
		closeServer()
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Nodes) != 1 || !result.MachineSSHCapableByID["device-1"] || len(result.PresentationDeviceIDsByNodeID) != 0 {
			t.Fatal("ambiguous top-level alignment inferred evidence or changed legacy projection")
		}
	}
	client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+record+`],"ordinary":1,"ordinary":2}`, presentationTestPolicy)
	defer closeServer()
	result, err := client.Load(context.Background(), nil)
	if err != nil || len(result.PresentationDeviceIDsByNodeID) != 1 {
		t.Fatal("unknown fields affected evidence", err)
	}
	if got := tailscalePresentationDeviceIDs([]byte(`{"devices":[`+record+`]}`), 2); got != nil {
		t.Fatal("misaligned count accepted")
	}
	if got := tailscalePresentationDeviceIDs([]byte(strings.Repeat(" ", maxUpstreamResponseBody+1)), 0); got != nil {
		t.Fatal("unbounded raw view")
	}
}

func TestTailscalePresentationPreservesIncumbentDeviceErrors(t *testing.T) {
	for _, fields := range []string{`"id":true,"nodeId":"safe"`, `"id":{},"nodeId":"safe"`, `"id":[],"nodeId":"safe"`, `"nodeId":42`, `"id":"device-1","nodeId":42`, `"id":null`, `"id":" "`, `"nodeId":null`, `"id":01`, `"id":+1`} {
		client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+presentationTestRecord(fields)+`]}`, presentationTestPolicy)
		result, err := client.Load(context.Background(), nil)
		closeServer()
		var loadErr *controlPlaneLoadError
		if result != nil || !errors.As(err, &loadErr) || loadErr.Stage != controlPlaneStageDevices {
			t.Fatalf("incumbent invalid scalar/blank error changed for %s: %v", fields, err)
		}
	}
	for _, pair := range [][2]string{
		{`"id":"same"`, `"nodeId":"same"`},
		{`"id":42`, `"id":"42"`},
		{`"id":" same "`, `"nodeId":"same"`},
		{`"ID":"same"`, `"id":"same"`},
	} {
		client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+presentationTestRecord(pair[0])+`,`+presentationTestRecord(pair[1])+`]}`, presentationTestPolicy)
		result, err := client.Load(context.Background(), nil)
		closeServer()
		if result != nil || err == nil || !strings.Contains(err.Error(), "duplicate id") {
			t.Fatal("typed kinds weakened legacy duplicate rejection", err)
		}
	}
}

func TestTailscalePresentationOmitsAllCollidingAssociations(t *testing.T) {
	a := ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "same"}
	b := ProviderDeviceID{controlPlaneTailscale, deviceIDKindNodeID, "same"}
	got := presentationDeviceIDsByNodeID([]Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, []ProviderDeviceID{a, a, b, a})
	if !reflect.DeepEqual(got, map[string]ProviderDeviceID{"c": b}) {
		t.Fatalf("collision picked a winner: %#v", got)
	}
	if got := presentationDeviceIDsByNodeID([]Node{{ID: "same"}, {ID: "same"}}, []ProviderDeviceID{a, b}); len(got) != 0 {
		t.Fatal("colliding legacy keys picked a winner")
	}
	if got := presentationDeviceIDsByNodeID([]Node{{ID: "a"}}, nil); got != nil {
		t.Fatal("misaligned projection")
	}
}

func TestTailscalePresentationSnapshotPropagationAndRetention(t *testing.T) {
	u := newTestUpstreams(t)
	client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+presentationTestRecord(`"nodeId":"node-1"`)+`]}`, presentationTestPolicy)
	defer closeServer()
	want := map[string]ProviderDeviceID{"node-1": {controlPlaneTailscale, deviceIDKindNodeID, "node-1"}}
	loaded, err := client.fetchDevices(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	} // Tagged fixture intentionally needs no owner lookup.
	provider, err := client.Load(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewCache(client, u.npm, time.Minute, discardLogger())
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := cache.Get()
	oneShot, names, err := loadHostnameSuggestionSnapshot(context.Background(), client, u.npm, emptyServiceMetadata())
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []map[string]ProviderDeviceID{loaded.PresentationDeviceIDsByNodeID, provider.PresentationDeviceIDsByNodeID, first.PresentationDeviceIDsByNodeID, oneShot.PresentationDeviceIDsByNodeID} {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("lost provenance: %#v", got)
		}
	}
	if !reflect.DeepEqual(names, []string{"server.tailnet.ts.net", "server"}) {
		t.Fatal("one-shot names changed")
	}
	// All new containers explicitly exclude internal identity evidence from JSON.
	for _, value := range []any{loaded, provider, first, oneShot} {
		if snapshot, ok := value.(*CacheData); ok {
			// Metadata already has non-JSON struct-keyed lookup maps. Omit
			// that unrelated object on a detached fixture copy, not evidence
			// or fields on the published snapshot under test.
			copy := *snapshot
			copy.ServiceMetadata = nil
			value = &copy
		}
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), "PresentationDeviceIDsByNodeID") || strings.Contains(string(encoded), `"Kind"`) || strings.Contains(string(encoded), `"Value"`) {
			t.Fatal("identity evidence leaked through JSON", err, string(encoded))
		}
	}
	u.proxyHostsFail.Store(true)
	if err := cache.refresh(context.Background()); err == nil || cache.Get() != first {
		t.Fatal("NPM failure replaced complete side evidence")
	}
	cold := NewCache(client, u.npm, time.Minute, discardLogger())
	if err := cold.refresh(context.Background()); err == nil || cold.Get() != nil {
		t.Fatal("NPM failure published partial cold evidence")
	}
	u.proxyHostsFail.Store(false)
	bad, closeBad := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[],"hasMore":true}`, presentationTestPolicy)
	defer closeBad()
	cache.controlPlane = bad
	if err := cache.refresh(context.Background()); err == nil || cache.Get() != first {
		t.Fatal("partial Devices response replaced snapshot")
	}
	unsupported, closeUnsupported := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+presentationTestRecord(`"id":" node-1 "`)+`]}`, presentationTestPolicy)
	defer closeUnsupported()
	cache.controlPlane = unsupported
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.Get() == first || len(cache.Get().PresentationDeviceIDsByNodeID) != 0 || !cache.Get().MachineSSHCapableByID["node-1"] {
		t.Fatal("successful evidence suppression retained stale ID or lost capability")
	}
	cache.controlPlane = u.hs
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.Get().PresentationDeviceIDsByNodeID != nil {
		t.Fatal("Headscale acquired typed Tailscale evidence")
	}
	hs, _, err := loadHostnameSuggestionSnapshot(context.Background(), u.hs, u.npm, emptyServiceMetadata())
	if err != nil || hs.PresentationDeviceIDsByNodeID != nil {
		t.Fatal("Headscale one-shot acquired evidence", err)
	}
}
