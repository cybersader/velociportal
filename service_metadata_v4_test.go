package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const metadataV4Fixture = `{"version":4,"namespace":"portal-demo-01","control_plane":"tailscale","services":[{"proxy_host_id":99,"name":null,"url":"https://unseen.example/path?q=1#part","category":"Unseen","order":0},{"proxy_host_id":3,"name":"Old Wiki","url":null,"category":"Tools","order":8,"icon":"generic"},{"proxy_host_id":100,"name":null,"url":null,"icon":"grafana"}],"machines":[{"device_id_type":"nodeId","device_id":"same","icon":"portainer"},{"device_id_type":"id","device_id":"same","icon":"generic"}],"applications":[{"target_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","icon":"immich"},{"target_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","icon":"generic"}]}`

func metadataV4(t *testing.T) *ServiceMetadata {
	t.Helper()
	m, err := parseServiceMetadata([]byte(metadataV4Fixture))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func metadataV4Wire(services, machines, apps string) string {
	return `{"version":4,"namespace":"portal-demo-01","control_plane":"tailscale","services":` + services + `,"machines":` + machines + `,"applications":` + apps + `}`
}
func requireMetadataInvalid(t *testing.T, source string) {
	t.Helper()
	if _, err := parseServiceMetadata([]byte(source)); err == nil {
		t.Fatal("invalid document accepted")
	}
}
func TestServiceMetadataUnicodeSyntaxAllVersions(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		for _, field := range []string{"name", "url"} {
			for _, scalar := range []string{`\ud800`, `\udc00`, `\ud800x`, string([]byte{0xff})} {
				t.Run(fmt.Sprintf("v%d-%s-%x", version, field, scalar), func(t *testing.T) {
					value := scalar
					if field == "url" {
						value = "https://example.test/" + value
					}
					row := `{"proxy_host_id":1,"` + field + `":"` + value + `"}`
					source := fmt.Sprintf(`{"version":%d,"services":[%s]}`, version, row)
					if version == 4 {
						source = metadataV4Wire("["+row+"]", "[]", "[]")
					}
					requireMetadataInvalid(t, source)
				})
			}
		}
		valid := fmt.Sprintf(`{"version":%d,"services":[{"proxy_host_id":1,"name":"😀"}]}`, version)
		if version == 4 {
			valid = metadataV4Wire(`[{"proxy_host_id":1,"name":"😀"}]`, "[]", "[]")
		}
		// Construct literal JSON escape bytes, not a second UTF-8 emoji.
		backslash := string([]byte{92})
		escaped := strings.Replace(valid, "😀", backslash+"ud83d"+backslash+"ude00", 1)
		if escaped == valid || !strings.Contains(escaped, backslash+"ud83d"+backslash+"ude00") {
			t.Fatal("fixture lacks escaped surrogate pair")
		}
		for _, source := range []string{valid, escaped} {
			metadata, err := parseServiceMetadata([]byte(source))
			if err != nil {
				t.Fatal("paired scalar rejected", err)
			}
			if metadata.Overrides[1].Name != "😀" {
				t.Fatal("paired scalar changed")
			}
		}
	}
}

func TestServiceMetadataV4Schema(t *testing.T) {
	m := metadataV4(t)
	if len(m.MachineIcons) != 2 || len(m.ApplicationIcons) != 2 || len(m.Overrides) != 3 {
		t.Fatal("incomplete indexes")
	}
	for _, icon := range serviceIconIDs {
		t.Run("icon-"+icon, func(t *testing.T) {
			d := cloneServiceMetadataDocument(m.document)
			d.Services[1].Icon = &icon
			d.Machines[0].Icon = icon
			d.Applications[0].Icon = icon
			raw, err := serializeServiceMetadataDocument(d)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = parseServiceMetadata(raw); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, provider := range []string{"headscale", "tailscale"} {
		t.Run(provider+"-empty", func(t *testing.T) {
			s := strings.Replace(metadataV4Wire("[]", "[]", "[]"), `"tailscale"`, `"`+provider+`"`, 1)
			m, err := parseServiceMetadata([]byte(s))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := serializeServiceMetadataDocument(m.document)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"services", "machines", "applications"} {
				if !bytes.Contains(raw, []byte(`"`+field+`": []`)) {
					t.Fatal("empty array became null")
				}
			}
		})
	}
	headscale := strings.Replace(metadataV4Wire(`[{"proxy_host_id":1,"icon":"generic"}]`, "[]", "[]"), `"tailscale"`, `"headscale"`, 1)
	if _, err := parseServiceMetadata([]byte(headscale)); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2, 3, 4} {
		for _, name := range []string{"", `,"name":null`, `,"name":"Résumé"`} {
			for _, link := range []string{"", `,"url":null`, `,"url":"https://example.test/path?q=1#frag"`} {
				t.Run(fmt.Sprintf("presence-v%d-%s-%s", version, name, link), func(t *testing.T) {
					entry := `{"proxy_host_id":1` + name + link + `}`
					source := fmt.Sprintf(`{"version":%d,"services":[%s]}`, version, entry)
					if version == 4 {
						source = metadataV4Wire("["+entry+"]", "[]", "[]")
					}
					m, err := parseServiceMetadata([]byte(source))
					valid := strings.Contains(name, `"Résumé"`) || strings.Contains(link, "https:") || version == 4 && (name != "" || link != "")
					if !valid {
						if err == nil {
							t.Fatal("empty/null-only legacy accepted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					raw, err := serializeServiceMetadataDocument(m.document)
					if err != nil {
						t.Fatal(err)
					}
					again, err := parseServiceMetadata(raw)
					if err != nil || !reflect.DeepEqual(m.document, again.document) {
						t.Fatal("presence lost", err, string(raw))
					}
				})
			}
		}
	}
	for _, value := range []string{"A", strings.Repeat("a", 128), "0", "042", "A_b-C"} {
		for _, kind := range []string{"id", "nodeId"} {
			source := metadataV4Wire("[]", fmt.Sprintf(`[{"device_id_type":%q,"device_id":%q,"icon":"generic"}]`, kind, value), "[]")
			if _, err := parseServiceMetadata([]byte(source)); err != nil {
				t.Fatal("supported ID rejected", err)
			}
		}
	}
	for _, order := range []int{0, maxServiceMetadataOrder} {
		s := metadataV4Wire(fmt.Sprintf(`[{"proxy_host_id":1,"category":%q,"order":%d,"name":%q}]`, strings.Repeat("é", 64), order, strings.Repeat("é", 120)), "[]", "[]")
		if _, err := parseServiceMetadata([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServiceMetadataV4StrictFields(t *testing.T) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadataV4Fixture), &root); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "namespace", "control_plane", "services", "machines", "applications"} {
		for _, bad := range []string{"missing", "null", "case", "duplicate", "case-duplicate"} {
			t.Run(field+"-"+bad, func(t *testing.T) {
				fields := map[string]json.RawMessage{}
				for k, v := range root {
					fields[k] = v
				}
				switch bad {
				case "missing":
					delete(fields, field)
				case "null":
					fields[field] = json.RawMessage("null")
				case "case":
					fields[strings.ToUpper(field)] = fields[field]
					delete(fields, field)
				}
				raw, _ := json.Marshal(fields)
				if bad == "duplicate" || bad == "case-duplicate" {
					key := field
					if bad == "case-duplicate" {
						key = strings.ToUpper(key)
					}
					raw = append(raw[:len(raw)-1], []byte(fmt.Sprintf(",%q:%s}", key, root[field]))...)
				}
				requireMetadataInvalid(t, string(raw))
			})
		}
	}
	entries := map[string]string{
		"services":     `{"proxy_host_id":1,"name":"Name","url":"https://example.test/","category":"Tools","order":0,"icon":"generic"}`,
		"machines":     `{"device_id_type":"id","device_id":"abc","icon":"generic"}`,
		"applications": `{"target_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","icon":"generic"}`,
	}
	for array, entry := range entries {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(entry), &fields)
		for field := range fields {
			for _, bad := range []string{"case", "duplicate", "case-duplicate", "wrong-shape", "null", "missing"} {
				if array == "services" && (bad == "missing" && field != "proxy_host_id" || bad == "null" && (field == "name" || field == "url")) {
					continue
				}
				t.Run(array+"-"+field+"-"+bad, func(t *testing.T) {
					f := map[string]json.RawMessage{}
					for k, v := range fields {
						f[k] = v
					}
					switch bad {
					case "case":
						f[strings.ToUpper(field)] = f[field]
						delete(f, field)
					case "missing":
						delete(f, field)
					case "null":
						f[field] = json.RawMessage("null")
					case "wrong-shape":
						f[field] = json.RawMessage("{}")
					}
					raw, _ := json.Marshal(f)
					if bad == "duplicate" || bad == "case-duplicate" {
						key := field
						if bad == "case-duplicate" {
							key = strings.ToUpper(key)
						}
						raw = append(raw[:len(raw)-1], []byte(fmt.Sprintf(",%q:%s}", key, fields[field]))...)
					}
					arrays := map[string]string{"services": "[]", "machines": "[]", "applications": "[]"}
					arrays[array] = "[" + string(raw) + "]"
					requireMetadataInvalid(t, metadataV4Wire(arrays["services"], arrays["machines"], arrays["applications"]))
				})
			}
		}
		for _, bad := range []string{"null", "{}", `[null]`, "[" + entry + "," + entry + "]", "[" + strings.TrimSuffix(entry, "}") + `,"unknown":true}]`} {
			arrays := map[string]string{"services": "[]", "machines": "[]", "applications": "[]"}
			arrays[array] = bad
			requireMetadataInvalid(t, metadataV4Wire(arrays["services"], arrays["machines"], arrays["applications"]))
		}
	}
	for _, replacement := range []string{`""`, `" portal"`, `"UPPER"`, `"-start"`, `"é"`, `"` + strings.Repeat("a", 65) + `"`} {
		requireMetadataInvalid(t, strings.Replace(metadataV4Fixture, `"portal-demo-01"`, replacement, 1))
	}
	for _, provider := range []string{"HEADSCALE", "Tailscale", "unknown", "headscale"} {
		requireMetadataInvalid(t, strings.Replace(metadataV4Fixture, `"tailscale"`, `"`+provider+`"`, 1))
	}
	for _, value := range []string{"", strings.Repeat("a", 129), " a", "a ", "-a", "a.b", "a/b", "a\n", "é", "a\x00"} {
		requireMetadataInvalid(t, metadataV4Wire("[]", fmt.Sprintf(`[{"device_id_type":"id","device_id":%q,"icon":"generic"}]`, value), "[]"))
	}
	for _, kind := range []string{"ID", "nodeid", "NodeId", "unknown", ""} {
		requireMetadataInvalid(t, strings.Replace(metadataV4Fixture, `"nodeId"`, `"`+kind+`"`, 1))
	}
	for _, digest := range []string{"", "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 63), "sha256:" + strings.Repeat("a", 65), "sha256:" + strings.Repeat("g", 64), "SHA256:" + strings.Repeat("a", 64)} {
		requireMetadataInvalid(t, metadataV4Wire("[]", "[]", fmt.Sprintf(`[{"target_digest":%q,"icon":"generic"}]`, digest)))
	}
	for _, icon := range []string{"", "unknown", "../icon", "https://icons.test/x", "data:image/svg+xml,x", "<svg/>"} {
		requireMetadataInvalid(t, strings.Replace(metadataV4Fixture, `"portainer"`, fmt.Sprintf("%q", icon), 1))
	}
	for _, field := range []string{"proxy_host_id", "order"} {
		for _, number := range []string{`"1"`, "1e0", "1.0", "-1", "9223372036854775808", "null"} {
			row := `{"proxy_host_id":1,"order":0,"name":"N"}`
			old := `"proxy_host_id":1`
			if field == "order" {
				old = `"order":0`
			}
			requireMetadataInvalid(t, metadataV4Wire("["+strings.Replace(row, old, `"`+field+`":`+number, 1)+"]", "[]", "[]"))
		}
	}
	for _, row := range []string{`{"proxy_host_id":0,"name":"N"}`, `{"proxy_host_id":1}`, `{"proxy_host_id":1,"order":1000001}`, `{"proxy_host_id":1,"name":"\ud800"}`, `{"proxy_host_id":1,"name":"\udc00"}`, `{"proxy_host_id":1,"url":"https://example.test/\ud800"}`, `{"proxy_host_id":1,"category":"\udc00"}`} {
		requireMetadataInvalid(t, metadataV4Wire("["+row+"]", "[]", "[]"))
	}
	badUTF8 := strings.Replace(metadataV4Fixture, "Old Wiki", string([]byte{0xff}), 1)
	requireMetadataInvalid(t, badUTF8)
	for _, suffix := range []string{" {}", " null", "/*comment*/", ","} {
		requireMetadataInvalid(t, metadataV4Fixture+suffix)
	}
	deep := strings.Repeat("[", 17) + "0" + strings.Repeat("]", 17)
	requireMetadataInvalid(t, strings.TrimSuffix(metadataV4Fixture, "}")+`,"unknown":`+deep+`}`)
	// Escaped delimiters/brackets inside valid strings are not nesting.
	source := metadataV4Wire(`[{"proxy_host_id":1,"name":"[[[ \"ok\" 😀","url":null}]`, "[]", "[]")
	if _, err := parseServiceMetadata([]byte(source)); err != nil {
		t.Fatal(err)
	}
}

func TestServiceMetadataV4Bounds(t *testing.T) {
	d := metadataV4(t).document
	d.Services = []serviceMetadataEntry{}
	d.Machines = []machineIconEntry{}
	d.Applications = []applicationIconEntry{}
	icon := "generic"
	for i := 0; i < 342; i++ {
		d.Services = append(d.Services, serviceMetadataEntry{ProxyHostID: i + 1, NameNull: true})
	}
	for i := 0; i < 341; i++ {
		d.Machines = append(d.Machines, machineIconEntry{deviceIDKindID, fmt.Sprint(i), icon})
		d.Applications = append(d.Applications, applicationIconEntry{fmt.Sprintf("sha256:%064x", i), icon})
	}
	raw, err := serializeServiceMetadataDocument(d)
	if err != nil {
		t.Fatal("1024 rejected", err)
	}
	if _, err = parseServiceMetadata(raw); err != nil {
		t.Fatal(err)
	}
	d.Applications = append(d.Applications, applicationIconEntry{fmt.Sprintf("sha256:%064x", 9999), icon})
	if _, err := serializeServiceMetadataDocument(d); err == nil {
		t.Fatal("1025 serialized")
	}
	raw, _ = json.Marshal(d)
	requireMetadataInvalid(t, string(raw))
	d.Applications = d.Applications[:341]
	before, _ := serializeServiceMetadataDocument(d)
	if _, err := mutateServiceMetadataIcon(d, 9999, icon); err == nil {
		t.Fatal("capacity silently evicted row")
	}
	if _, err := mutateMachineMetadataIcon(d, ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "new"}, icon); err == nil {
		t.Fatal("capacity silently evicted machine")
	}
	if _, err := mutateApplicationMetadataIcon(d, fmt.Sprintf("sha256:%064x", 9999), icon); err == nil {
		t.Fatal("capacity silently evicted application")
	}
	after, _ := serializeServiceMetadataDocument(d)
	if !bytes.Equal(before, after) {
		t.Fatal("capacity mutated source")
	}
	raw = []byte(metadataV4Wire("[]", "[]", "[]"))
	raw = append(raw, bytes.Repeat([]byte(" "), maxServiceMetadataBytes-len(raw))...)
	if _, err := parseServiceMetadata(raw); err != nil {
		t.Fatal("exact byte limit rejected", err)
	}
	requireMetadataInvalid(t, string(append(raw, ' ')))
	d = metadataV4(t).document
	d.Services = []serviceMetadataEntry{}
	name := strings.Repeat("x", 120)
	link := "https://example.test/" + strings.Repeat("x", 2000)
	for i := 0; i < 128; i++ {
		d.Services = append(d.Services, serviceMetadataEntry{ProxyHostID: i + 1, Name: &name, URL: &link})
	}
	if _, err := serializeServiceMetadataDocument(d); err == nil {
		t.Fatal("oversized encoding accepted")
	}
}

func TestServiceMetadataV4CloneAndEncoding(t *testing.T) {
	m := metadataV4(t)
	d := m.document
	raw, err := serializeServiceMetadataDocument(d)
	if err != nil {
		t.Fatal(err)
	}
	round, err := parseServiceMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := serializeServiceMetadataDocument(round.document)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("nondeterministic", err)
	}
	if round.document.Services[0].ProxyHostID != 3 || round.document.Machines[0].DeviceIDType != deviceIDKindID || !strings.HasSuffix(round.document.Applications[0].TargetDigest, strings.Repeat("0", 64)) {
		t.Fatal("wrong sort")
	}
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.HasSuffix(raw, []byte("\n\n")) || !bytes.HasPrefix(raw, []byte("{\n  \"version\": 4,\n  \"namespace\":")) {
		t.Fatal("wire formatting")
	}
	copy := cloneServiceMetadataDocument(d)
	*copy.Services[0].URL = "https://changed.test/"
	*copy.Services[0].Category = "Changed"
	*copy.Services[0].Order = 99
	copy.Services[0].NameNull = false
	*copy.Services[1].Name = "Changed"
	*copy.Services[1].Icon = "immich"
	copy.Machines[0].Icon = "generic"
	copy.Applications[0].Icon = "generic"
	unchanged, _ := serializeServiceMetadataDocument(m.document)
	if !bytes.Equal(raw, unchanged) {
		t.Fatal("clone aliases published document")
	}
	*m.Overrides[99].Order = 123
	if *m.document.Services[0].Order != 0 {
		t.Fatal("index aliases document order")
	}
	round.MachineIcons[ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "same"}] = "gitea"
	round.ApplicationIcons[round.document.Applications[0].TargetDigest] = "gitea"
	if m.MachineIcons[ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "same"}] != "generic" || m.ApplicationIcons[round.document.Applications[0].TargetDigest] != "generic" {
		t.Fatal("parse indexes alias")
	}
	for _, version := range []int{1, 2, 3, 4} {
		down := cloneServiceMetadataDocument(d)
		down.Version = version
		if _, err := serializeServiceMetadataDocumentV3(down); err == nil {
			t.Fatal("legacy encoder downcast")
		}
		if version != 4 {
			if _, err := serializeServiceMetadataDocument(down); err == nil {
				t.Fatal("generic encoder downcast")
			}
		}
	}
	for _, field := range []string{"name", "url"} {
		bad := cloneServiceMetadataDocument(d)
		value := "https://example.test/"
		if field == "name" {
			bad.Services[0].Name = &value
			bad.Services[0].NameNull = true
		} else {
			bad.Services[0].URLNull = true
		}
		if _, err := serializeServiceMetadataDocument(bad); err == nil {
			t.Fatal("contradictory presence accepted")
		}
	}
	invalid := string([]byte{0xff})
	bad := cloneServiceMetadataDocument(d)
	bad.Services[0].URL = &invalid
	if _, err := serializeServiceMetadataDocument(bad); err == nil {
		t.Fatal("invalid Unicode normalized")
	}
	// v1 remains an explicit proposal fragment, not the complete-model encoder.
	if _, err := serializeServiceMetadataDocumentV1(d.Services); err == nil {
		t.Fatal("proposal discarded unsupported fields")
	}
}

func TestServiceMetadataV4IconOnlyPreservation(t *testing.T) {
	original := metadataV4(t).document
	before, _ := serializeServiceMetadataDocument(original)
	for _, icon := range []string{"gitea", ""} {
		t.Run("npm-"+icon, func(t *testing.T) {
			d, err := mutateServiceMetadataIcon(original, 100, icon)
			if err != nil {
				t.Fatal(err)
			}
			expected := cloneServiceMetadataDocument(original)
			expected.Services[2].Icon = nil
			if icon != "" {
				expected.Services[2].Icon = &icon
			}
			if !reflect.DeepEqual(d, expected) {
				t.Fatal("NPM icon changed other fields")
			}
			if _, err := serializeServiceMetadataDocument(d); err != nil {
				t.Fatal("null-only preservation rejected", err)
			}
		})
		t.Run("machine-"+icon, func(t *testing.T) {
			key := ProviderDeviceID{controlPlaneTailscale, deviceIDKindNodeID, "same"}
			d, err := mutateMachineMetadataIcon(original, key, icon)
			if err != nil {
				t.Fatal(err)
			}
			expected := cloneServiceMetadataDocument(original)
			if icon == "" {
				expected.Machines = expected.Machines[1:]
			} else {
				expected.Machines[0].Icon = icon
			}
			if !reflect.DeepEqual(d, expected) {
				t.Fatal("machine icon changed other typed rows")
			}
		})
		t.Run("application-"+icon, func(t *testing.T) {
			d, err := mutateApplicationMetadataIcon(original, original.Applications[0].TargetDigest, icon)
			if err != nil {
				t.Fatal(err)
			}
			expected := cloneServiceMetadataDocument(original)
			if icon == "" {
				expected.Applications = expected.Applications[1:]
			} else {
				expected.Applications[0].Icon = icon
			}
			if !reflect.DeepEqual(d, expected) {
				t.Fatal("application icon changed unrelated fields")
			}
		})
	}
	for _, id := range []int{3, 99} {
		d, err := mutateServiceMetadataIcon(original, id, "gitea")
		if err != nil {
			t.Fatal(err)
		}
		for i := range d.Services {
			if d.Services[i].ProxyHostID == id {
				d.Services[i].Icon = original.Services[i].Icon
			}
		}
		if !reflect.DeepEqual(d, original) {
			t.Fatal("name/link/category/order/null lost")
		}
	}
	d, err := mutateServiceMetadataIcon(original, 200, "generic")
	if err != nil {
		t.Fatal(err)
	}
	d, err = mutateServiceMetadataIcon(d, 200, "")
	if err != nil || !reflect.DeepEqual(d, original) {
		t.Fatal("icon-only row not removed", err)
	}
	d, err = mutateServiceMetadataIcon(original, 200, "")
	if err != nil || !reflect.DeepEqual(d, original) {
		t.Fatal("absent reset changed document", err)
	}
	after, _ := serializeServiceMetadataDocument(original)
	if !bytes.Equal(before, after) {
		t.Fatal("source mutated")
	}
}

// Real bound configured startup; synthetic provider data, not live acceptance.
func metadataV4EditorFixture(t *testing.T) (*ServiceMetadataEditor, *Cache, http.Handler) {
	t.Helper()
	config := editorTestConfig()
	config.Binding = serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "portal-demo-01"}
	e, err := newServiceMetadataEditor(editorTestFile(t, metadataV4Fixture), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	data := standardTestData()
	data.ControlPlane.Provider = controlPlaneTailscale
	cache := newTestCache(data)
	return e, cache, newServiceMetadataEditorHandler(editorTestOperations(e, cache))
}
func TestServiceMetadataV4GuardedBrowserPreservation(t *testing.T) {
	for _, action := range []string{"save", "reset", "blank"} {
		t.Run(action, func(t *testing.T) {
			e, cache, h := metadataV4EditorFixture(t)
			old := e.view.Load()
			prior := cache.Get()
			stamp := prior.UpdatedAt
			f := editForm(t, h, "3", "alice@example.com")
			body := editBody(f, action)
			if action == "blank" {
				var fields map[string]string
				_ = json.Unmarshal([]byte(editBody(f, "save")), &fields)
				fields["name"] = ""
				fields["url"] = ""
				fields["icon"] = ""
				raw, _ := json.Marshal(fields)
				body = string(raw)
			}
			response := editHTTPRequest(h, "POST", "3", "alice@example.com", body, nil)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			current := e.view.Load()
			d := current.metadata.document
			if d.Version != 4 || d.Namespace != old.metadata.document.Namespace || d.ControlPlane != controlPlaneTailscale || !reflect.DeepEqual(d.Machines, []machineIconEntry{old.metadata.document.Machines[1], old.metadata.document.Machines[0]}) || !reflect.DeepEqual(current.metadata.MachineIcons, old.metadata.MachineIcons) || !reflect.DeepEqual(current.metadata.ApplicationIcons, old.metadata.ApplicationIcons) {
				t.Fatal("v4 data lost")
			}
			expected, err := mutateServiceMetadata(old.metadata.document, 3, "Shared Wiki", "https://shared.example/path", "generic", false)
			if action != "save" {
				expected, err = mutateServiceMetadata(old.metadata.document, 3, "", "", "", action == "reset")
			}
			if err != nil {
				t.Fatal(err)
			}
			wanted, _ := serializeServiceMetadataDocument(expected)
			if !bytes.Equal(wanted, current.bytes) {
				t.Fatal("browser did not use lossless candidate")
			}
			disk, err := os.ReadFile(e.path)
			if err != nil || !bytes.Equal(disk, wanted) {
				t.Fatal("disk mismatch", err)
			}
			if !bytes.Equal(old.bytes, []byte(metadataV4Fixture)) || old.metadata.Overrides[3].Name != "Old Wiki" {
				t.Fatal("prior immutable view changed")
			}
			stale := *prior
			stale.ServiceMetadata = old.metadata
			cache.data.Store(&stale)
			local := e.requestData(cache.Get())
			if local.ServiceMetadata != current.metadata || local.UpdatedAt != stamp || cache.Get().ServiceMetadata != old.metadata {
				t.Fatal("old poll replaced complete metadata or timestamp")
			}
			// Held-directory reads retain the complete syntax; runtime parse still denies.
			raw, file, err := e.readFile()
			if err != nil {
				t.Fatal(err)
			}
			_ = file.Close()
			reread, err := parseServiceMetadata(raw)
			if err != nil || !reflect.DeepEqual(reread.document, d) {
				t.Fatal("held reread lost data", err)
			}
			if _, err := parseBoundRuntimeServiceMetadata(raw, e.config.Binding); err != nil {
				t.Fatal("bound reread rejected saved v4", err)
			}
		})
	}
}

func TestServiceMetadataShrinkingEdits(t *testing.T) {
	link := "https://example.test/" + strings.Repeat("&", 2027)
	rows := make([]string, 22)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"proxy_host_id":%d,"url":"%s"}`, i+1, link)
	}
	for _, version := range []int{1, 2, 3, 4} {
		source := fmt.Sprintf(`{"version":%d,"services":[%s]}`, version, strings.Join(rows, ","))
		if version == 4 {
			source = metadataV4Wire("["+strings.Join(rows, ",")+"]", "[]", "[]")
		}
		for _, action := range []string{"reset", "blank", "smaller", "unchanged"} {
			t.Run(fmt.Sprintf("v%d-%s", version, action), func(t *testing.T) {
				m, err := parseServiceMetadata([]byte(source))
				if err != nil || len(source) >= maxServiceMetadataBytes {
					t.Fatal("compact source must be valid", err)
				}
				if _, err := serializeServiceMetadataDocument(m.document); err == nil {
					t.Fatal("fixture must exceed canonical output bound before shrinking")
				}
				var e *ServiceMetadataEditor
				var h http.Handler
				if version == 4 {
					e, _, h = metadataV4EditorFixture(t)
					if err := os.WriteFile(e.path, []byte(source), 0600); err != nil {
						t.Fatal(err)
					}
					e.view.Store(&serviceMetadataEditorView{metadata: m, bytes: []byte(source), revision: e.digest("file", []byte(source))})
				} else {
					// Real legacy startup accepts the compact input, with no fixture bypass.
					e, err = newServiceMetadataEditor(editorTestFile(t, source), editorTestConfig())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = e.Close() })
					data := standardTestData()
					data.ControlPlane.Provider = controlPlaneHeadscale
					h = newServiceMetadataEditorHandler(editorTestOperations(e, newTestCache(data)))
				}
				before := e.view.Load()
				form := editForm(t, h, "3", "alice@example.com")
				fields := map[string]string{"action": "save", "revision": form.Revision, "target_revision": form.TargetRevision, "csrf": form.CSRF, "name": "", "url": "", "icon": ""}
				switch action {
				case "reset":
					fields = map[string]string{"action": "reset", "revision": form.Revision, "target_revision": form.TargetRevision, "csrf": form.CSRF}
				case "smaller":
					fields["url"] = "https://example.test/small"
				case "unchanged":
					fields["url"] = link
				}
				body, _ := json.Marshal(fields)
				response := editHTTPRequest(h, "POST", "3", "alice@example.com", string(body), nil)
				disk, err := os.ReadFile(e.path)
				if err != nil {
					t.Fatal(err)
				}
				if action == "unchanged" {
					if response.Code != 400 || !bytes.Equal(disk, before.bytes) || e.view.Load() != before {
						t.Fatal("oversized final output must fail without a write", response.Code)
					}
					return
				}
				if response.Code != 200 || len(disk) > maxServiceMetadataBytes {
					t.Fatal("valid shrinking edit rejected", response.Code, response.Body.String())
				}
				current, err := parseServiceMetadata(disk)
				if err != nil || !bytes.Equal(disk, e.view.Load().bytes) {
					t.Fatal("saved candidate invalid", err)
				}
				wantVersion := 3
				if version == 4 {
					wantVersion = 4
				}
				if current.document.Version != wantVersion {
					t.Fatal("version changed incorrectly")
				}
				for id, override := range current.Overrides {
					if id != 3 && override.URL != link {
						t.Fatal("unselected row changed")
					}
				}
				if action == "smaller" {
					if len(current.Overrides) != 22 || current.Overrides[3].URL != fields["url"] {
						t.Fatal("smaller save mismatch")
					}
				} else if len(current.Overrides) != 21 {
					t.Fatal("reset did not remove selected row")
				}
				if !bytes.Equal(before.bytes, []byte(source)) {
					t.Fatal("mutated old immutable view")
				}
			})
		}
	}
}

func TestServiceMetadataMutationRejectsInvalidOriginalModel(t *testing.T) {
	for _, bad := range []string{"duplicate", "id", "empty", "presence", "order", "version", "machine", "application", "legacy-field"} {
		t.Run(bad, func(t *testing.T) {
			d := metadataV4(t).document
			switch bad {
			case "duplicate":
				d.Services = append(d.Services, d.Services[1])
			case "id":
				d.Services[1].ProxyHostID = 0
			case "empty":
				d.Services[1] = serviceMetadataEntry{ProxyHostID: 3}
			case "presence":
				d.Services[1].NameNull = true
			case "order":
				value := -1
				d.Services[1].Order = &value
			case "version":
				d.Version = 5
			case "machine":
				d.Machines[0].Icon = "unknown"
			case "application":
				d.Applications[0].TargetDigest = "bad"
			case "legacy-field":
				d = serviceMetadataDocument{Version: 1, Services: []serviceMetadataEntry{{ProxyHostID: 3, Name: &[]string{"valid"}[0], Icon: &[]string{"generic"}[0]}}}
			}
			if _, err := mutateServiceMetadata(d, 3, "", "", "", true); err == nil {
				t.Fatal("reset hid invalid original model")
			}
			if _, err := mutateServiceMetadataIcon(d, 3, ""); err == nil {
				t.Fatal("icon reset hid invalid original model")
			}
		})
	}
}

func TestServiceMetadataV4RuntimeGate(t *testing.T) {
	path := editorTestFile(t, metadataV4Fixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, load := range []struct {
		name string
		run  func() error
	}{
		{"readonly", func() error { _, err := loadServiceMetadataFile(path); return err }},
		{"loader-used-by-doctor-and-hostname", func() error { _, err := serviceMetadataLoaderForPath(path)(); return err }},
		{"editor-first-read", func() error {
			e, err := newServiceMetadataEditor(path, editorTestConfig())
			if e != nil {
				_ = e.Close()
			}
			return err
		}},
		{"diagnostics", func() error { return inspectServiceMetadataEditorStorage(path) }},
	} {
		t.Run(load.name, func(t *testing.T) {
			err := load.run()
			if err == nil {
				t.Fatal("runtime accepted v4")
			}
			for _, private := range []string{path, "portal-demo-01", "same", "sha256:"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("diagnostic leaked value")
				}
			}
		})
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("startup rewrote file")
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 || files[0].Name() != filepath.Base(path) {
		t.Fatal("diagnostics/startup created lock or probe")
	}
	e, _, _ := editorFixture(t)
	old := e.view.Load()
	d := metadataV4(t).document
	if err := e.commit(old.revision, d, serviceMetadataWriteNPM, func() error { return nil }); err == nil {
		t.Fatal("legacy writer created v4")
	}
	disk, _ := os.ReadFile(e.path)
	if !bytes.Equal(disk, old.bytes) || e.view.Load() != old {
		t.Fatal("blocked upgrade changed state")
	}
}

func TestServiceMetadataV4CandidateBeforeFilesystem(t *testing.T) {
	for _, bad := range []string{"nil-final", "namespace", "provider", "machine", "application", "unseen-npm", "presence", "oversized"} {
		t.Run(bad, func(t *testing.T) {
			e, _, _ := metadataV4EditorFixture(t)
			old := e.view.Load()
			d := cloneServiceMetadataDocument(old.metadata.document)
			final := func() error { t.Fatal("invalid candidate reached final check"); return nil }
			switch bad {
			case "nil-final":
				final = nil
			case "namespace":
				d.Namespace = "different"
			case "provider":
				d.ControlPlane = controlPlaneHeadscale
			case "machine":
				d.Machines[1].DeviceID = "invalid id"
			case "application":
				d.Applications[1].Icon = "not-finite"
			case "unseen-npm":
				value := " bad "
				d.Services[0].Name = &value
			case "presence":
				d.Services[1].NameNull = true
			case "oversized":
				name := strings.Repeat("x", 120)
				link := "https://example.test/" + strings.Repeat("x", 2000)
				for i := 0; i < 128; i++ {
					d.Services = append(d.Services, serviceMetadataEntry{ProxyHostID: 1000 + i, Name: &name, URL: &link})
				}
			}
			// A valid candidate would fail here with a path error. Candidate validation
			// must win first, without opening a path, creating a temp or running final.
			directory := e.directory
			e.directory = nil
			err := e.commit(old.revision, d, serviceMetadataWriteNPM, final)
			e.directory = directory
			if err == nil {
				t.Fatal("invalid candidate committed")
			}
			if bad != "nil-final" && bad != "namespace" && bad != "provider" && errors.Is(err, errMetadataStorage) {
				t.Fatal("filesystem reached before validation")
			}
			disk, _ := os.ReadFile(e.path)
			if !bytes.Equal(disk, old.bytes) || e.view.Load() != old {
				t.Fatal("invalid candidate changed state")
			}
			files, _ := os.ReadDir(filepath.Dir(e.path))
			if len(files) != 2 {
				t.Fatal("temporary file leaked")
			}
		})
	}
}

func TestServiceMetadataV4CommitUncertainty(t *testing.T) {
	for _, stage := range []string{"create", "write", "filesync", "close", "recheck", "rename", "rename-not-applied", "rename-applied", "dirsync", "publish"} {
		t.Run(stage, func(t *testing.T) {
			e, _, h := metadataV4EditorFixture(t)
			old := e.view.Load()
			f := editForm(t, h, "3", "alice@example.com")
			uncertain := stage == "rename-not-applied" || stage == "rename-applied" || stage == "dirsync" || stage == "publish"
			e.failpoint = func(current string) error {
				if current == stage {
					return errors.New("fixture")
				}
				return nil
			}
			if strings.HasPrefix(stage, "rename-") {
				e.rename = func(a int, b string, c int, d string) error {
					if stage == "rename-applied" {
						if err := unix.Renameat(a, b, c, d); err != nil {
							t.Fatal(err)
						}
					}
					return errors.New("ambiguous result")
				}
			}
			response := editHTTPRequest(h, "POST", "3", "alice@example.com", editBody(f, "save"), nil)
			disk, err := os.ReadFile(e.path)
			if err != nil {
				t.Fatal(err)
			}
			changed := stage == "rename-applied" || stage == "dirsync" || stage == "publish"
			if bytes.Equal(disk, old.bytes) == changed || e.view.Load() != old {
				t.Fatal("unexpected disk/confirmed-memory outcome")
			}
			if uncertain {
				if response.Code != 503 || !strings.Contains(response.Body.String(), "uncertain") || !e.disabled {
					t.Fatal("uncertainty not explicit", response.Code)
				}
				e.failpoint = nil
				e.rename = nil
				for _, method := range []string{"GET", "POST"} {
					r := editHTTPRequest(h, method, "3", "alice@example.com", editBody(f, "save"), nil)
					if r.Code != 503 || strings.Contains(r.Body.String(), "revision") || strings.Contains(r.Body.String(), "Old Wiki") {
						t.Fatal("uncertain editable state exposed", r.Code, r.Body.String())
					}
				}
			} else if response.Code != 500 || e.disabled {
				t.Fatal("known pre-rename failure misclassified", response.Code)
			}
			temporary, _ := filepath.Glob(filepath.Join(filepath.Dir(e.path), ".services.json.tmp-*"))
			if len(temporary) != 0 {
				t.Fatal("temporary leaked")
			}
			// Synthetic configured restart reconciles actual disk, not the old view.
			path, config := e.path, e.config
			_ = e.Close()
			restarted, err := newServiceMetadataEditor(path, config)
			if err != nil {
				t.Fatal("bound restart failed", err)
			}
			defer restarted.Close()
			if !bytes.Equal(restarted.view.Load().bytes, disk) || restarted.disabled {
				t.Fatal("restart did not reconcile actual disk")
			}
		})
	}
}

func TestServiceMetadataV4CrossSourceAndExternalCAS(t *testing.T) {
	for _, source := range []string{"npm", "machine", "application", "external", "target"} {
		t.Run(source, func(t *testing.T) {
			e, cache, h := metadataV4EditorFixture(t)
			f := editForm(t, h, "3", "alice@example.com")
			old := e.view.Load()
			d := old.metadata.document
			var err error
			switch source {
			case "npm":
				d, err = mutateServiceMetadata(d, 99, "Different", "", "", false)
			case "machine":
				d, err = mutateMachineMetadataIcon(d, ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "same"}, "gitea")
			case "application":
				d, err = mutateApplicationMetadataIcon(d, d.Applications[0].TargetDigest, "gitea")
			case "external":
				err = os.WriteFile(e.path, append(append([]byte(nil), old.bytes...), '\n'), 0o600)
			case "target":
				snapshot := *cache.Get()
				snapshot.ProxyHosts = append([]ProxyHost(nil), snapshot.ProxyHosts...)
				snapshot.ProxyHosts[2].ForwardPort = 8443
				cache.data.Store(&snapshot)
			}
			if err != nil {
				t.Fatal(err)
			}
			if source != "external" && source != "target" {
				e.mu.Lock()
				kind := serviceMetadataWriteIcon
				if source == "npm" {
					kind = serviceMetadataWriteNPM
				}
				err = e.commit(old.revision, d, kind, func() error { return nil })
				e.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			confirmed := e.view.Load()
			disk, _ := os.ReadFile(e.path)
			response := editHTTPRequest(h, "POST", "3", "alice@example.com", editBody(f, "save"), nil)
			after, _ := os.ReadFile(e.path)
			if response.Code != 409 || !bytes.Equal(disk, after) || e.view.Load() != confirmed {
				t.Fatal("CAS conflict lost update", response.Code)
			}
		})
	}
}

func TestServiceMetadataV4FinalAuthority(t *testing.T) {
	for _, change := range []string{"freshness", "visibility", "csrf"} {
		t.Run(change, func(t *testing.T) {
			e, cache, h := metadataV4EditorFixture(t)
			f := editForm(t, h, "3", "alice@example.com")
			old := e.view.Load()
			// Expired initial CSRF is checked by the real adapter; existing dedicated
			// late-expiry test also exercises the final callback using its clock seam.
			if change == "csrf" {
				f.CSRF = e.csrf("alice@example.com", time.Now().Add(-2*serviceMetadataCSRFDuration))
			}
			e.failpoint = func(stage string) error {
				if stage == "recheck" {
					d := *cache.Get()
					if change == "freshness" {
						d.UpdatedAt = time.Now().Add(-time.Hour)
					}
					if change == "visibility" {
						d.Policy = &Policy{}
					}
					cache.data.Store(&d)
				}
				return nil
			}
			response := editHTTPRequest(h, "POST", "3", "alice@example.com", editBody(f, "save"), nil)
			expected := map[string]int{"freshness": 503, "visibility": 404, "csrf": 403}[change]
			disk, _ := os.ReadFile(e.path)
			if response.Code != expected || e.view.Load() != old || !bytes.Equal(disk, old.bytes) {
				t.Fatal("authority not rechecked", response.Code)
			}
		})
	}
}
