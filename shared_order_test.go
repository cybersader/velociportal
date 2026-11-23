package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Same evidence as sharedTargetFixture, with a caller-selected shared document.
// Alice sees NPM IDs 1 and 3; ID 2 is visible only to bob; carol is no editor.
func sharedOrderFixture(t *testing.T, raw string) (*mcpApplicationTools, *SharedIconOperations, *http.Request) {
	t.Helper()
	config := editorTestConfig()
	config.Binding = serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "selected-source"}
	e, err := newServiceMetadataEditor(editorTestFile(t, raw), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	cache := newTestCache(nil)
	data := machineMatcherFixture(t)
	npm := standardTestData()
	data.ProxyHosts, data.Policy.ACLs, data.Policy.Groups, data.Policy.Hosts = npm.ProxyHosts, npm.Policy.ACLs, npm.Policy.Groups, npm.Policy.Hosts
	data.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{"node-1": {Provider: controlPlaneTailscale, Kind: deviceIDKindID, Value: "node-1"}}
	now := time.Now().Truncate(time.Second)
	data.UpdatedAt = now
	cache.data.Store(data)
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	cfg := &Config{PortalEditMode: serviceEditShared, ControlPlane: controlPlaneTailscale, StateNamespace: e.config.Binding.Namespace,
		TrustedProxyCIDR: trusted, PollInterval: 30 * time.Second, MCP: &MCPConfig{Origin: e.config.Origin, Host: e.config.Host}}
	o := newSharedIconOperations(cfg, e, cache, nil)
	o.now = func() time.Time { return now }
	tools := newMCPApplicationTools(cfg, o, cache, nil)
	if tools.shared != o {
		t.Fatal("fixture did not bind shared operations")
	}
	tools.now = o.now
	return tools, o, sharedTargetRequest(t, o, "alice@example.com")
}

const sharedOrderV4 = `{"version":4,"namespace":"selected-source","control_plane":"tailscale","services":[{"proxy_host_id":3,"name":"Shared Wiki","url":null,"icon":"grafana","category":"Tools","order":8},{"proxy_host_id":99,"name":null,"url":"https://unseen.example/path"}],"machines":[{"device_id_type":"id","device_id":"node-1","icon":"generic"}],"applications":[{"target_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","icon":"immich"}]}`

func sharedOrderNPM(o *SharedIconOperations, id string) map[string]any {
	ref := o.sharedTargetHandle("alice@example.com", sharedTargetNPM, id)
	return map[string]any{"kind": string(ref.Kind), "handle": ref.Handle}
}

func sharedOrderCall(t *testing.T, a *mcpApplicationTools, r *http.Request, name string, args any) (map[string]any, mcpToolResult) {
	t.Helper()
	result, err := a.Call(r, name, s4hJSON(t, args))
	if err != nil {
		t.Fatal(name, err)
	}
	var out map[string]any
	if json.Unmarshal(s4hStructured(t, result), &out) != nil {
		t.Fatal("structured result")
	}
	return out, result
}

func sharedOrderGet(t *testing.T, a *mcpApplicationTools, r *http.Request, target map[string]any) map[string]any {
	t.Helper()
	out, result := sharedOrderCall(t, a, r, "shared_target_get", map[string]any{"target": target})
	if result.IsError {
		t.Fatal("get", out)
	}
	return out
}

// The written file must equal the canonical encoding of the expected document,
// proving every untouched field, array, binding and null presence survived.
func sharedOrderExpectDisk(t *testing.T, o *SharedIconOperations, expected string) {
	t.Helper()
	metadata, err := parseBoundRuntimeServiceMetadata([]byte(expected), o.editor.config.Binding)
	if err != nil {
		t.Fatal("expected fixture", err)
	}
	want, err := serializeServiceMetadataDocument(metadata.document)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(o.editor.path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("disk mismatch:\n got %s\nwant %s", got, want)
	}
	if !bytes.Equal(o.editor.view.Load().bytes, want) {
		t.Fatal("confirmed view differs from disk")
	}
}

func TestSharedOrderMCPDiscoveryAndSchema(t *testing.T) {
	a, _, r := sharedOrderFixture(t, sharedOrderV4)
	list, err := a.List(r)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s4hNames(t, list), ","); got != "icons_list,shared_targets_list,shared_target_get,shared_icon_set,shared_icon_reset,shared_order_set" {
		t.Fatal("discovery", got)
	}
	d := mcpApplicationTool("shared_order_set")
	input := d["inputSchema"].(map[string]any)
	entries := input["properties"].(map[string]any)["entries"].(map[string]any)
	if entries["type"] != "array" || entries["minItems"] != 1 || entries["maxItems"] != maxSharedOrderEntries {
		t.Fatal("entries bounds", entries)
	}
	item := entries["items"].(map[string]any)
	if item["additionalProperties"] != false || !reflect.DeepEqual(item["required"], []string{"target"}) || item["minProperties"] != 2 ||
		!reflect.DeepEqual(s4hKeys(item["properties"].(map[string]any)), []string{"category", "order", "target"}) {
		t.Fatal("entry schema", item)
	}
	out := d["outputSchema"].(map[string]any)
	if !reflect.DeepEqual(s4hKeys(out["properties"].(map[string]any)), []string{"revision", "status"}) || !reflect.DeepEqual(out["required"], []string{"status", "revision"}) {
		t.Fatal("output schema", out)
	}
	for _, name := range []string{"shared_targets_list", "shared_target_get"} {
		schema := mcpApplicationTool(name)["outputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		if name == "shared_targets_list" {
			props = props["targets"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		}
		if props["category"] == nil || props["order"] == nil {
			t.Fatal("category/order not described", name)
		}
	}
}

func TestSharedOrderMCPMultiEntryCommitAndReadBack(t *testing.T) {
	a, o, r := sharedOrderFixture(t, sharedOrderV4)
	page, result := sharedOrderCall(t, a, r, "shared_targets_list", map[string]any{"kind": "npm"})
	if result.IsError || len(page["targets"].([]any)) != 2 {
		t.Fatal("alice should see exactly NPM 1 and 3", page)
	}
	three, one := sharedOrderNPM(o, "3"), sharedOrderNPM(o, "1")
	before := sharedOrderGet(t, a, r, three)
	if before["category"] != "Tools" || before["order"] != float64(8) {
		t.Fatal("current shared order not exposed", before)
	}
	if _, present := sharedOrderGet(t, a, r, one)["order"]; present {
		t.Fatal("absent order must be omitted")
	}
	args := map[string]any{"revision": before["revision"], "entries": []any{
		map[string]any{"target": three, "order": 2},
		map[string]any{"target": one, "order": 0, "category": "Media"},
	}}
	saved, result := sharedOrderCall(t, a, r, "shared_order_set", args)
	if result.IsError || saved["status"] != "saved" || saved["revision"] == before["revision"] || saved["revision"] != o.editor.view.Load().revision {
		t.Fatal("save", saved)
	}
	// Omitted category on 3 is unchanged; 1 gains a new entry with only order/category.
	sharedOrderExpectDisk(t, o, strings.Replace(strings.Replace(sharedOrderV4, `"order":8`, `"order":2`, 1),
		`"services":[`, `"services":[{"proxy_host_id":1,"category":"Media","order":0},`, 1))
	if got := sharedOrderGet(t, a, r, three); got["category"] != "Tools" || got["order"] != float64(2) || got["revision"] != saved["revision"] || got["shared_icon"] != "grafana" {
		t.Fatal("read back 3", got)
	}
	if got := sharedOrderGet(t, a, r, one); got["category"] != "Media" || got["order"] != float64(0) {
		t.Fatal("read back 1 (order zero must be present)", got)
	}
	page, _ = sharedOrderCall(t, a, r, "shared_targets_list", map[string]any{"kind": "npm"})
	if encoded := string(s4hJSON(t, page)); !strings.Contains(encoded, `"category":"Media"`) || !strings.Contains(encoded, `"order":2`) {
		t.Fatal("list does not expose order", encoded)
	}
	// The committed shared order is what the portal groups/sorts by.
	if got := o.editor.view.Load().metadata.Overrides[1]; got.Category != "Media" || got.Order == nil || *got.Order != 0 {
		t.Fatal("render metadata", got)
	}
	// Replaying with the consumed revision conflicts and changes nothing.
	view := o.editor.view.Load()
	if out, result := sharedOrderCall(t, a, r, "shared_order_set", args); !result.IsError || out["error"] != "conflict" {
		t.Fatal("replay", out)
	}
	sharedNPMUnchanged(t, o.editor, view, view.bytes)
}

func TestSharedOrderMCPNullRemovesAndOmittedKeeps(t *testing.T) {
	a, o, r := sharedOrderFixture(t, sharedOrderV4)
	three, one := sharedOrderNPM(o, "3"), sharedOrderNPM(o, "1")
	rev := sharedOrderGet(t, a, r, three)["revision"]
	saved, result := sharedOrderCall(t, a, r, "shared_order_set", map[string]any{"revision": rev, "entries": []any{map[string]any{"target": one, "order": 4}}})
	if result.IsError {
		t.Fatal(saved)
	}
	saved, result = sharedOrderCall(t, a, r, "shared_order_set", map[string]any{"revision": saved["revision"], "entries": []any{
		map[string]any{"target": three, "order": nil},
		map[string]any{"target": one, "order": nil},
	}})
	if result.IsError {
		t.Fatal("null removal", saved)
	}
	// 3 keeps category and every other field; 1 had only order, so its entry goes.
	sharedOrderExpectDisk(t, o, strings.Replace(sharedOrderV4, `,"order":8`, ``, 1))
	got := sharedOrderGet(t, a, r, three)
	if _, present := got["order"]; present || got["category"] != "Tools" {
		t.Fatal("null removal read back", got)
	}
	saved, result = sharedOrderCall(t, a, r, "shared_order_set", map[string]any{"revision": saved["revision"], "entries": []any{map[string]any{"target": three, "category": nil}}})
	if result.IsError {
		t.Fatal(saved)
	}
	sharedOrderExpectDisk(t, o, strings.Replace(sharedOrderV4, `,"category":"Tools","order":8`, ``, 1))
	// Setting values that are already current succeeds without writing.
	view := o.editor.view.Load()
	saved, result = sharedOrderCall(t, a, r, "shared_order_set", map[string]any{"revision": saved["revision"], "entries": []any{map[string]any{"target": one, "order": nil, "category": nil}}})
	if result.IsError || saved["revision"] != view.revision {
		t.Fatal("no-op", saved)
	}
	sharedNPMUnchanged(t, o.editor, view, view.bytes)
}

func TestSharedOrderLegacyDocumentBecomesV3Losslessly(t *testing.T) {
	legacy := `{"version":2,"services":[{"proxy_host_id":3,"name":"Old","url":null,"category":"Tools","order":8},{"proxy_host_id":99,"name":null,"url":"https://unseen.example"}]}`
	a, o, r := sharedOrderFixture(t, legacy)
	rev := sharedOrderGet(t, a, r, sharedOrderNPM(o, "3"))["revision"]
	saved, result := sharedOrderCall(t, a, r, "shared_order_set", map[string]any{"revision": rev, "entries": []any{map[string]any{"target": sharedOrderNPM(o, "3"), "order": 1, "category": "Home"}}})
	if result.IsError {
		t.Fatal(saved)
	}
	sharedOrderExpectDisk(t, o, `{"version":3,"services":[{"proxy_host_id":3,"name":"Old","url":null,"category":"Home","order":1},{"proxy_host_id":99,"name":null,"url":"https://unseen.example"}]}`)
}

func TestSharedOrderMCPRejectsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(o *SharedIconOperations, rev any) map[string]any
		want string
	}{
		{"stale-revision", func(o *SharedIconOperations, _ any) map[string]any {
			return map[string]any{"revision": "stale", "entries": []any{map[string]any{"target": sharedOrderNPM(o, "3"), "order": 1}}}
		}, "conflict"},
		{"invisible", func(o *SharedIconOperations, rev any) map[string]any {
			return map[string]any{"revision": rev, "entries": []any{map[string]any{"target": sharedOrderNPM(o, "2"), "order": 1}}}
		}, "not_found"},
		{"invisible-before-conflict", func(o *SharedIconOperations, _ any) map[string]any {
			return map[string]any{"revision": "stale", "entries": []any{map[string]any{"target": sharedOrderNPM(o, "2"), "order": 1}}}
		}, "not_found"},
		{"one-invisible-aborts-all", func(o *SharedIconOperations, rev any) map[string]any {
			return map[string]any{"revision": rev, "entries": []any{map[string]any{"target": sharedOrderNPM(o, "3"), "order": 1}, map[string]any{"target": sharedOrderNPM(o, "2"), "order": 2}}}
		}, "not_found"},
		{"missing-id", func(o *SharedIconOperations, rev any) map[string]any {
			return map[string]any{"revision": rev, "entries": []any{map[string]any{"target": sharedOrderNPM(o, "404"), "order": 1}}}
		}, "not_found"},
		{"other-callers-handle", func(o *SharedIconOperations, rev any) map[string]any {
			ref := o.sharedTargetHandle("bob@example.com", sharedTargetNPM, "3")
			return map[string]any{"revision": rev, "entries": []any{map[string]any{"target": ref, "order": 1}}}
		}, "not_found"},
		{"machine", func(o *SharedIconOperations, rev any) map[string]any {
			ref := o.sharedTargetHandle("alice@example.com", sharedTargetMachine, "id", "node-1")
			return map[string]any{"revision": rev, "entries": []any{map[string]any{"target": sharedOrderNPM(o, "3"), "order": 1}, map[string]any{"target": ref, "order": 1}}}
		}, "not_found"},
		{"application", func(o *SharedIconOperations, rev any) map[string]any {
			ref := o.sharedTargetHandle("alice@example.com", sharedTargetApplication, "ps_x", "sha256:x")
			return map[string]any{"revision": rev, "entries": []any{map[string]any{"target": ref, "category": "Apps"}}}
		}, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, o, r := sharedOrderFixture(t, sharedOrderV4)
			rev := sharedOrderGet(t, a, r, sharedOrderNPM(o, "3"))["revision"]
			view := o.editor.view.Load()
			out, result := sharedOrderCall(t, a, r, "shared_order_set", tc.args(o, rev))
			if !result.IsError || out["error"] != tc.want {
				t.Fatalf("got %v want %s", out, tc.want)
			}
			sharedNPMUnchanged(t, o.editor, view, view.bytes)
		})
	}
}

func TestSharedOrderMCPRequiresSharedEditor(t *testing.T) {
	for _, change := range []string{"non-editor", "allowlist-removed", "origin", "writer-uncertain", "stale"} {
		t.Run(change, func(t *testing.T) {
			a, o, r := sharedOrderFixture(t, sharedOrderV4)
			rev := sharedOrderGet(t, a, r, sharedOrderNPM(o, "3"))["revision"]
			args := map[string]any{"revision": rev, "entries": []any{map[string]any{"target": sharedOrderNPM(o, "3"), "order": 1}}}
			want := "forbidden"
			switch change {
			case "non-editor":
				r = sharedTargetRequest(t, o, "carol@example.com")
			case "allowlist-removed":
				o.editor.config.Logins = map[string]bool{"bob@example.com": true}
			case "origin":
				o.editor.config.Origin = "https://other.example"
			case "writer-uncertain":
				o.editor.disabled = true
				want = "uncertain"
			case "stale":
				d := *o.cache.Get()
				d.UpdatedAt = d.UpdatedAt.Add(-91 * time.Second)
				o.cache.data.Store(&d)
				want = "unavailable"
			}
			view := o.editor.view.Load()
			out, result := sharedOrderCall(t, a, r, "shared_order_set", args)
			if !result.IsError || out["error"] != want {
				t.Fatalf("got %v want %s", out, want)
			}
			sharedNPMUnchanged(t, o.editor, view, view.bytes)
			list, err := a.List(r)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range s4hNames(t, list) {
				if name == "shared_order_set" {
					t.Fatal("ineligible caller discovered shared_order_set")
				}
			}
		})
	}
}

// Visibility loss between preparation and rename fails the whole commit.
func TestSharedOrderFinalRecheckBeforeRename(t *testing.T) {
	a, o, r := sharedOrderFixture(t, sharedOrderV4)
	rev := sharedOrderGet(t, a, r, sharedOrderNPM(o, "3"))["revision"]
	view := o.editor.view.Load()
	o.editor.failpoint = func(stage string) error {
		if stage == "rename" {
			d := *o.cache.Get()
			hosts := append([]ProxyHost(nil), d.ProxyHosts...)
			for i := range hosts {
				if hosts[i].ID == 1 {
					hosts[i].Enabled = false
				}
			}
			d.ProxyHosts = hosts
			o.cache.data.Store(&d)
		}
		return nil
	}
	out, result := sharedOrderCall(t, a, r, "shared_order_set", map[string]any{"revision": rev, "entries": []any{
		map[string]any{"target": sharedOrderNPM(o, "3"), "order": 1},
		map[string]any{"target": sharedOrderNPM(o, "1"), "order": 2},
	}})
	if !result.IsError || out["error"] != "not_found" {
		t.Fatal("final recheck", out)
	}
	sharedNPMUnchanged(t, o.editor, view, view.bytes)
}

func TestSharedOrderTypedOperationRejectsDuplicates(t *testing.T) {
	_, o, r := sharedOrderFixture(t, sharedOrderV4)
	ref := o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "3")
	order := 1
	view := o.editor.view.Load()
	entry := mcpSharedOrderEntry{Target: ref, SetOrder: true, Order: &order}
	if _, err := o.SetMCPOrder(r, mcpSharedOrderRequest{Revision: view.revision, Entries: []mcpSharedOrderEntry{entry, entry}}); !errors.Is(err, errMetadataInvalidFields) {
		t.Fatal("duplicate typed targets", err)
	}
	if _, err := o.SetMCPOrder(r, mcpSharedOrderRequest{Revision: view.revision, Entries: []mcpSharedOrderEntry{{Target: ref}}}); !errors.Is(err, errMetadataInvalidFields) {
		t.Fatal("empty change", err)
	}
	sharedNPMUnchanged(t, o.editor, view, view.bytes)
	if _, _, err := mutateServiceMetadataOrder(view.metadata.document, []sharedOrderChange{{id: 3, entry: entry}, {id: 3, entry: entry}}); err == nil {
		t.Fatal("pure mutator accepted duplicate IDs")
	}
}

func TestSharedOrderStrictArguments(t *testing.T) {
	target := map[string]any{"kind": "npm", "handle": strings.Repeat("A", 43)}
	other := map[string]any{"kind": "npm", "handle": strings.Repeat("E", 43)}
	valid := func(entries ...any) map[string]any { return map[string]any{"revision": "opaque", "entries": entries} }
	for _, args := range []map[string]any{
		valid(map[string]any{"target": target, "order": 0}),
		valid(map[string]any{"target": target, "order": maxServiceMetadataOrder}),
		valid(map[string]any{"target": target, "order": nil}),
		valid(map[string]any{"target": target, "category": strings.Repeat("é", maxServiceMetadataCategory)}),
		valid(map[string]any{"target": target, "category": nil}),
		valid(map[string]any{"target": target, "order": 1, "category": "Tools"}, map[string]any{"target": other, "category": "Tools"}),
		valid(map[string]any{"target": map[string]any{"kind": "machine", "handle": strings.Repeat("A", 43)}, "order": 1}),
	} {
		if _, err := mcpApplicationArguments("shared_order_set", s4hJSON(t, args)); err != nil {
			t.Fatal("valid arguments rejected", string(s4hJSON(t, args)))
		}
	}
	tooMany := []any{}
	for i := 0; i <= maxSharedOrderEntries; i++ {
		tooMany = append(tooMany, map[string]any{"target": map[string]any{"kind": "npm", "handle": fmt.Sprintf("%042dA", i)}, "order": i})
	}
	for label, raw := range map[string]string{
		"empty":              `[]`,
		"too-many":           string(s4hJSON(t, tooMany)),
		"null":               `null`,
		"object":             `{}`,
		"null-item":          `[null]`,
		"scalar-item":        `[1]`,
		"no-change":          `[{"target":` + string(s4hJSON(t, target)) + `}]`,
		"no-target":          `[{"order":1}]`,
		"null-target":        `[{"target":null,"order":1}]`,
		"extra":              `[{"target":` + string(s4hJSON(t, target)) + `,"order":1,"icon":"generic"}]`,
		"meta":               `[{"target":` + string(s4hJSON(t, target)) + `,"order":1,"_meta":{}}]`,
		"case":               `[{"target":` + string(s4hJSON(t, target)) + `,"Order":1}]`,
		"duplicate-key":      `[{"target":` + string(s4hJSON(t, target)) + `,"order":1,"order":2}]`,
		"duplicate-target":   `[{"target":` + string(s4hJSON(t, target)) + `,"order":1},{"target":` + string(s4hJSON(t, target)) + `,"order":2}]`,
		"order-string":       `[{"target":` + string(s4hJSON(t, target)) + `,"order":"1"}]`,
		"order-float":        `[{"target":` + string(s4hJSON(t, target)) + `,"order":1.0}]`,
		"order-exponent":     `[{"target":` + string(s4hJSON(t, target)) + `,"order":1e1}]`,
		"order-negative":     `[{"target":` + string(s4hJSON(t, target)) + `,"order":-1}]`,
		"order-negative-0":   `[{"target":` + string(s4hJSON(t, target)) + `,"order":-0}]`,
		"order-too-large":    `[{"target":` + string(s4hJSON(t, target)) + `,"order":1000001}]`,
		"order-bool":         `[{"target":` + string(s4hJSON(t, target)) + `,"order":true}]`,
		"category-empty":     `[{"target":` + string(s4hJSON(t, target)) + `,"category":""}]`,
		"category-padded":    `[{"target":` + string(s4hJSON(t, target)) + `,"category":" Tools "}]`,
		"category-control":   `[{"target":` + string(s4hJSON(t, target)) + `,"category":"a\u0000b"}]`,
		"category-long":      `[{"target":` + string(s4hJSON(t, target)) + `,"category":"` + strings.Repeat("x", maxServiceMetadataCategory+1) + `"}]`,
		"category-number":    `[{"target":` + string(s4hJSON(t, target)) + `,"category":5}]`,
		"category-surrogate": `[{"target":` + string(s4hJSON(t, target)) + `,"category":"\ud800"}]`,
		"bad-handle":         `[{"target":{"kind":"npm","handle":"short"},"order":1}]`,
	} {
		t.Run(label, func(t *testing.T) {
			raw := `{"revision":"opaque","entries":` + raw + `}`
			if _, err := mcpApplicationArguments("shared_order_set", []byte(raw)); !errors.Is(err, errMCPInvalidParams) {
				t.Fatal("accepted invalid entries", raw)
			}
			var a *mcpApplicationTools
			if _, err := a.Call(nil, "shared_order_set", []byte(raw)); !errors.Is(err, errMCPInvalidParams) {
				t.Fatal("invalid entries reached operation")
			}
		})
	}
	// A full 64-entry request with maximal ASCII categories fits the unchanged
	// 16 KiB request limit.
	full := []any{}
	for i := 0; i < maxSharedOrderEntries; i++ {
		full = append(full, map[string]any{"target": map[string]any{"kind": "npm", "handle": fmt.Sprintf("%042dA", i)}, "order": maxServiceMetadataOrder, "category": strings.Repeat("x", maxServiceMetadataCategory)})
	}
	request := s4hJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "shared_order_set", "arguments": map[string]any{"revision": strings.Repeat("r", 256), "entries": full}}})
	if len(request) > mcpMaxRequestBytes {
		t.Fatal("maximal ASCII request exceeds MCP request limit", len(request))
	}
	if _, err := mcpApplicationArguments("shared_order_set", s4hJSON(t, map[string]any{"revision": "opaque", "entries": full})); err != nil {
		t.Fatal("maximal valid entries rejected", err)
	}
}

// Production mux, IdentityMiddleware and JSON-RPC dispatch over a configured
// legacy file: read current order, set it, read back, and refuse a replay.
func TestS5SharedOrderThroughProductionDispatch(t *testing.T) {
	f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 2, nil))
	p := f.page(t, sharedTargetNPM)
	if len(p.Targets) != 1 || p.Targets[0].Category != "Tools" || p.Targets[0].Order != "0" {
		t.Fatal("listing omitted current shared order", p.Targets)
	}
	form := f.form(t, p.Targets[0].Target)
	args := map[string]any{"revision": form.Revision, "entries": []any{map[string]any{"target": form.Target, "order": 7, "category": "Home"}}}
	var saved map[string]string
	if json.Unmarshal(f.call(t, "shared_order_set", args, ""), &saved) != nil || saved["status"] != "saved" || saved["revision"] == "" || saved["revision"] == form.Revision {
		t.Fatal("save", saved)
	}
	after := f.form(t, form.Target)
	if after.Category != "Home" || after.Order != "7" || after.Revision != saved["revision"] || after.Name != "Original" {
		t.Fatal("read back", after)
	}
	disk := s5Bytes(t, f.editor.path)
	for _, want := range []string{`"version": 3`, `"proxy_host_id": 99`, `"name": "Unseen"`, `"url": null`, `"category": "Home"`, `"order": 7`} {
		if !bytes.Contains(disk, []byte(want)) {
			t.Fatalf("lossless legacy save missing %s: %s", want, disk)
		}
	}
	f.call(t, "shared_order_set", args, "conflict")
	if !bytes.Equal(disk, s5Bytes(t, f.editor.path)) {
		t.Fatal("replay wrote")
	}
}
