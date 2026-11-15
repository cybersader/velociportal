package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestMCPLifecycleAndIcons(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, offer := range []string{mcpProtocolVersion, "2025-03-26", "2099-01-01"} {
		body := `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"` + offer + `","capabilities":{},"clientInfo":{"name":"synthetic-probe","version":"1"}}}`
		w := mcpTestServe(t, handler, mcpTestRequest(body, false))
		var response struct {
			ID     string `json:"id"`
			Result struct {
				ProtocolVersion string                     `json:"protocolVersion"`
				Capabilities    map[string]json.RawMessage `json:"capabilities"`
			} `json:"result"`
		}
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != 200 || response.ID != "init" || response.Result.ProtocolVersion != mcpProtocolVersion || len(response.Result.Capabilities) != 1 || response.Result.Capabilities["tools"] == nil {
			t.Fatalf("initialize %s: %d %s", offer, w.Code, w.Body.String())
		}
	}
	w := mcpTestServe(t, handler, mcpTestRequest(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, true))
	if w.Code != 202 || w.Body.Len() != 0 {
		t.Fatalf("initialized: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{"ping", "tools/list", "tools/call"} {
		params := "{}"
		if method == "tools/call" {
			params = `{"name":"icons_list","arguments":{}}`
		}
		body := `{"jsonrpc":"2.0","id":0,"method":"` + method + `","params":` + params + `}`
		r := mcpTestRequest(body, true)
		r.Header.Set("Tailscale-User-Login", "bob@example.com") // No retained initialize permission.
		w := mcpTestServe(t, handler, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
		if method == "tools/list" {
			var response struct {
				Result struct {
					Tools []struct {
						Name        string          `json:"name"`
						Input       map[string]any  `json:"inputSchema"`
						Output      map[string]any  `json:"outputSchema"`
						Annotations map[string]bool `json:"annotations"`
					} `json:"tools"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Result.Tools) != 1 {
				t.Fatal("unexpected tool surface")
			}
			tool := response.Result.Tools[0]
			if tool.Name != "icons_list" || tool.Input["type"] != "object" || tool.Input["additionalProperties"] != false || tool.Output["type"] != "object" || !tool.Annotations["readOnlyHint"] || tool.Annotations["openWorldHint"] {
				t.Fatalf("tool contract: %+v", tool)
			}
		}
		if method == "tools/call" {
			var response struct {
				Result struct {
					Structured struct {
						Icons []struct {
							ID    string `json:"id"`
							Label string `json:"label"`
						} `json:"icons"`
					} `json:"structuredContent"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			result := response.Result
			if len(result.Structured.Icons) != len(serviceIcons) || len(result.Content) != 1 || result.Content[0].Type != "text" || result.IsError {
				t.Fatal("invalid tool result")
			}
			structured, _ := json.Marshal(result.Structured)
			if string(structured) != result.Content[0].Text {
				t.Fatal("structured/text mismatch")
			}
			for i, icon := range serviceIcons {
				if result.Structured.Icons[i].ID != icon.ID || result.Structured.Icons[i].Label != icon.Label {
					t.Fatal("registry mismatch")
				}
			}
			if strings.Contains(w.Body.String(), "@") || strings.Contains(w.Body.String(), "/static/") || strings.Contains(w.Body.String(), "ts.net") {
				t.Fatal("private or unnecessary data in tool response")
			}
		}
	}
	// Reconnect/new process needs no stored session or initial handshake state.
	fresh := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	w = mcpTestServe(t, fresh, mcpTestRequest(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"icons_list"}}`, true))
	if w.Code != 200 {
		t.Fatal("stateless reconnect failed")
	}
	r := mcpTestRequest(`{"jsonrpc":"2.0","id":4,"method":"ping"}`, true)
	r.Header.Del("Tailscale-User-Login")
	if w := mcpTestServe(t, handler, r); w.Code != 401 {
		t.Fatal("initialize bypassed per-request authentication")
	}
}

func TestMCPJSONRPCErrors(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, test := range []struct {
		name, body   string
		status, code int
	}{
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, 400, -32600},
		{"null", `null`, 400, -32600},
		{"response-not-request", `{"jsonrpc":"2.0","id":1,"result":{}}`, 400, -32600},
		{"version", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, 400, -32600},
		{"null-id", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, 400, -32600},
		{"float-id", `{"jsonrpc":"2.0","id":1.2,"method":"ping"}`, 400, -32600},
		{"exponent-id", `{"jsonrpc":"2.0","id":1e2,"method":"ping"}`, 400, -32600},
		{"large-id", `{"jsonrpc":"2.0","id":9007199254740992,"method":"ping"}`, 400, -32600},
		{"object-id", `{"jsonrpc":"2.0","id":{},"method":"ping"}`, 400, -32600},
		{"null-params", `{"jsonrpc":"2.0","id":1,"method":"ping","params":null}`, 400, -32600},
		{"array-params", `{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`, 400, -32600},
		{"duplicate-id", `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`, 400, -32600},
		{"escaped-duplicate", `{"jsonrpc":"2.0","id":1,"method":"ping","method":"tools/list"}`, 400, -32600},
		{"case-alias", `{"jsonrpc":"2.0","id":1,"ID":2,"method":"ping"}`, 400, -32600},
		{"case-field", `{"jsonrpc":"2.0","id":1,"Method":"ping"}`, 400, -32600},
		{"unknown-field", `{"jsonrpc":"2.0","id":1,"method":"ping","extra":true}`, 400, -32600},
		{"nested-duplicate", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"a":1,"a":2}}}`, 400, -32600},
		{"ping-param", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"extra":true}}`, 200, -32602},
		{"meta-null", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":null}}`, 200, -32602},
		{"cursor", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"invented"}}`, 200, -32602},
		{"missing-init", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, 200, -32602},
		{"null-caps", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":null,"clientInfo":{"name":"x","version":"1"}}}`, 200, -32602},
		{"null-client", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":null}}`, 200, -32602},
		{"unknown-tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"personal_read"}}`, 200, -32602},
		{"arguments", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"icons_list","arguments":{"x":1}}}`, 200, -32602},
		{"arguments-null", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"icons_list","arguments":null}}`, 200, -32602},
		{"arguments-array", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"icons_list","arguments":[]}}`, 200, -32602},
		{"unknown-method", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, 200, -32601},
		{"mutation", `{"jsonrpc":"2.0","id":1,"method":"personal_create","params":{}}`, 200, -32601},
		{"notification-with-id", `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`, 200, -32601},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := mcpTestServe(t, handler, mcpTestRequest(test.body, true))
			var response struct {
				ID    json.RawMessage `json:"id"`
				Error mcpRPCError     `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != test.status || response.Error.Code != test.code {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if test.status == 400 && string(response.ID) != "null" {
				t.Fatal("invalid envelope got an ID")
			}
		})
	}
	for _, id := range []string{`""`, `"abc"`, `0`, `-1`, `9007199254740991`} {
		w := mcpTestServe(t, handler, mcpTestRequest(`{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`, true))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":`+id) {
			t.Fatalf("id %s: %s", id, w.Body.String())
		}
	}
}

func TestMCPNotificationsAndVersionDefaults(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, method := range []string{"notifications/initialized", "notifications/cancelled", "unknown/notification", "tools/call", "personal_create"} {
		body := `{"jsonrpc":"2.0","method":"` + method + `","params":{}}`
		w := mcpTestServe(t, handler, mcpTestRequest(body, true))
		if w.Code != 202 || w.Body.Len() != 0 {
			t.Fatalf("notification %s: %d %s", method, w.Code, w.Body.String())
		}
		w = mcpTestServe(t, handler, mcpTestRequest(body, false))
		if w.Code != 400 {
			t.Fatal("missing version did not use unsupported 2025-03-26 default")
		}
	}
	w := mcpTestServe(t, handler, mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"old","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`, false))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), mcpProtocolVersion) {
		t.Fatal("offered version not negotiated")
	}
}

func TestMCPJSONDepth(t *testing.T) {
	for _, depth := range []int{16, 17} {
		body := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		if (mcpValidateJSON([]byte(body)) == nil) != (depth == 16) {
			t.Fatalf("depth=%d", depth)
		}
	}
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	body := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"nested":` + strings.Repeat("[", 17) + "0" + strings.Repeat("]", 17) + `}}}`
	w := mcpTestServe(t, handler, mcpTestRequest(body, true))
	if w.Code != 400 {
		t.Fatal("deep request accepted")
	}
}
