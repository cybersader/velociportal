package main

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Positive authority always comes from the real middleware, never a test-injected
// Identity. Context substitution below is only a negative late-change fixture.
func mcpGuardRequest(t *testing.T) *http.Request {
	t.Helper()
	var admitted *http.Request
	r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"icons_list"}}`, true)
	IdentityMiddleware(mcpTrusted(), http.HandlerFunc(func(_ http.ResponseWriter, actual *http.Request) {
		admitted = actual
	})).ServeHTTP(httptest.NewRecorder(), r)
	if admitted == nil || IdentityFromContext(admitted.Context()) == nil {
		t.Fatal("real identity middleware did not admit fixture")
	}
	return admitted
}

type mcpGuardBody struct{ reads, closes int }

func (b *mcpGuardBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (b *mcpGuardBody) Close() error             { b.closes++; return nil }

func TestMCPToolRequestAllowedAndRepeatable(t *testing.T) {
	for _, origin := range []bool{false, true} {
		for _, site := range []string{"", "same-origin"} {
			for _, mode := range []string{"", "cors", "same-origin"} {
				for _, dest := range []string{"", "empty"} {
					t.Run(fmt.Sprintf("origin=%v/site=%s/mode=%s/dest=%s", origin, site, mode, dest), func(t *testing.T) {
						r := mcpGuardRequest(t)
						if origin {
							r.Header.Set("Origin", mcpTestOrigin)
						}
						for key, value := range map[string]string{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": mode, "Sec-Fetch-Dest": dest} {
							if value != "" {
								r.Header.Set(key, value)
							}
						}
						want := origin || site == "" && mode == "" && dest == ""
						body := &mcpGuardBody{}
						r.Body = body
						ctx, identity := r.Context(), IdentityFromContext(r.Context())
						h := &mcpHandler{config: *mcpTestConfig(), slots: make(chan struct{}, 8)}
						h.slots <- struct{}{}
						for i := 0; i < 3; i++ {
							if (checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()) == nil) != want || mcpAdmittedIdentity(r, *mcpTestConfig()) != want || h.admittedIdentity(r) != want {
								t.Fatal("native/browser admission table changed")
							}
						}
						if body.reads != 0 || body.closes != 0 || r.Body != body || r.Context() != ctx || IdentityFromContext(ctx) != identity || len(h.slots) != 1 {
							t.Fatal("repeat check consumed body or changed request/authority/slot")
						}
					})
				}
			}
		}
	}
	for _, content := range []string{"application/json", "application/json; charset=utf-8", "application/json; charset=UTF-8"} {
		for _, accept := range []string{"application/json, text/event-stream", "text/event-stream, application/json", "application/json, text/event-stream, text/plain"} {
			r := mcpGuardRequest(t)
			r.Header.Set("Content-Type", content)
			r.Header.Set("Accept", accept)
			r.ContentLength = -1
			r.TransferEncoding = []string{"chunked"}
			if err := checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMCPToolRequestLateChanges(t *testing.T) {
	changes := map[string]func(*http.Request){
		"untrusted-source":         func(r *http.Request) { r.RemoteAddr = "192.0.2.8:1234" },
		"malformed-source":         func(r *http.Request) { r.RemoteAddr = "not-an-ip" },
		"missing-context-identity": func(r *http.Request) { *r = *r.WithContext(context.Background()) },
		"different-context-identity": func(r *http.Request) {
			other := mcpTestRequest("", true)
			other.Header.Set("Tailscale-User-Login", "bob@example.com")
			IdentityMiddleware(mcpTrusted(), http.HandlerFunc(func(_ http.ResponseWriter, actual *http.Request) { *r = *r.WithContext(actual.Context()) })).ServeHTTP(httptest.NewRecorder(), other)
		},
		"mutated-context-login":   func(r *http.Request) { IdentityFromContext(r.Context()).Login = "bob@example.com" },
		"missing-login":           func(r *http.Request) { r.Header.Del("Tailscale-User-Login") },
		"changed-raw-login":       func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "bob@example.com") },
		"padded-login":            func(r *http.Request) { r.Header.Set("Tailscale-User-Login", " alice@example.com") },
		"host":                    func(r *http.Request) { r.Host = "evil.example" },
		"host-case":               func(r *http.Request) { r.Host = "Portal.tailnet.ts.net:8081" },
		"method":                  func(r *http.Request) { r.Method = "GET" },
		"preflight":               func(r *http.Request) { r.Method = "OPTIONS" },
		"path":                    func(r *http.Request) { r.URL.Path = "/other" },
		"slash":                   func(r *http.Request) { r.URL.Path = "/mcp/" },
		"encoded-alias":           func(r *http.Request) { r.URL.RawPath = "/%6dcp" },
		"query":                   func(r *http.Request) { r.URL.RawQuery = "secret=value" },
		"force-query":             func(r *http.Request) { r.URL.ForceQuery = true },
		"nil-url":                 func(r *http.Request) { r.URL = nil },
		"missing-version":         func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") },
		"wrong-version":           func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-03-26") },
		"empty-version":           func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "") },
		"missing-content":         func(r *http.Request) { r.Header.Del("Content-Type") },
		"form-content":            func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"content-parameter":       func(r *http.Request) { r.Header.Set("Content-Type", "application/json; foo=bar") },
		"content-charset":         func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=latin1") },
		"missing-accept":          func(r *http.Request) { r.Header.Del("Accept") },
		"json-only-accept":        func(r *http.Request) { r.Header.Set("Accept", "application/json") },
		"accept-q":                func(r *http.Request) { r.Header.Set("Accept", "application/json;q=1, text/event-stream") },
		"accept-duplicate-media":  func(r *http.Request) { r.Header.Set("Accept", "application/json, text/event-stream, application/json") },
		"accept-oversize":         func(r *http.Request) { r.Header.Set("Accept", strings.Repeat(" ", 1025)) },
		"transfer-conflict":       func(r *http.Request) { r.TransferEncoding = []string{"chunked"} },
		"transfer-unknown":        func(r *http.Request) { r.TransferEncoding = []string{"gzip"}; r.ContentLength = -1 },
		"transfer-multiple":       func(r *http.Request) { r.TransferEncoding = []string{"chunked", "chunked"}; r.ContentLength = -1 },
		"trailer-map":             func(r *http.Request) { r.Trailer = http.Header{"Private": []string{"secret"}} },
		"content-length-combined": func(r *http.Request) { r.Header.Set("Content-Length", "1, 2") },
		"content-length-empty":    func(r *http.Request) { r.Header.Set("Content-Length", "") },
		"content-length-oversize": func(r *http.Request) { r.ContentLength = mcpMaxRequestBytes + 1 },
		"cancelled": func(r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			*r = *r.WithContext(ctx)
			cancel()
		},
		"deadline-expired": func(r *http.Request) {
			ctx, cancel := context.WithDeadline(r.Context(), time.Unix(1, 0))
			t.Cleanup(cancel)
			*r = *r.WithContext(ctx)
		},
	}
	for _, browser := range []bool{false, true} {
		for name, change := range changes {
			t.Run(fmt.Sprintf("browser=%v/%s", browser, name), func(t *testing.T) {
				r := mcpGuardRequest(t)
				if browser {
					r.Header.Set("Origin", mcpTestOrigin)
					r.Header.Set("Sec-Fetch-Site", "same-origin")
					r.Header.Set("Sec-Fetch-Mode", "cors")
					r.Header.Set("Sec-Fetch-Dest", "empty")
				}
				if err := checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()); err != nil {
					t.Fatal(err)
				}
				body := &mcpGuardBody{}
				r.Body = body
				change(r)
				for i := 0; i < 2; i++ {
					err := checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted())
					if err == nil || err.Error() != "forbidden" {
						t.Fatalf("late change accepted or detailed error: %v", err)
					}
				}
				if body.reads != 0 || body.closes != 0 {
					t.Fatal("guard touched consumed body")
				}
			})
		}
	}
}

func TestMCPToolRequestRawHeaderNegatives(t *testing.T) {
	for key, values := range map[string][]string{
		"Origin":                     {"", "null", "http://evil.example", mcpTestOrigin + "/", mcpTestOrigin + ", " + mcpTestOrigin},
		"Sec-Fetch-Site":             {"", "none", "same-site", "cross-site"},
		"Sec-Fetch-Mode":             {"", "navigate", "no-cors", "websocket"},
		"Sec-Fetch-Dest":             {"", "document", "iframe"},
		"Tailscale-User-Login":       {"", "alice", "tag:server", "alice,bob@example.com", "alice@example.com ", "alice;bob@example.com"},
		"Tailscale-User-Name":        {"", "bad\x00name"},
		"Tailscale-User-Profile-Pic": {"", "bad\nvalue"},
		"Authorization":              {"", "Bearer secret"}, "Cookie": {"", "session=secret"}, "Mcp-Session-Id": {"", "secret"},
		"Access-Control-Request-Method": {"", "POST"}, "Access-Control-Request-Headers": {"", "Content-Type"},
		"Sec-Fetch-User": {"", "?1"}, "Content-Encoding": {"", "gzip"}, "Trailer": {"", "X-Trailer"}, "Transfer-Encoding": {"", "chunked"},
	} {
		for i, value := range values {
			t.Run(fmt.Sprintf("%s/%d", key, i), func(t *testing.T) {
				r := mcpGuardRequest(t)
				r.Header.Set("Origin", mcpTestOrigin)
				if err := checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()); err != nil {
					t.Fatal(err)
				}
				r.Header.Set(key, value)
				if err := checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()); err == nil || err.Error() != "forbidden" {
					t.Fatalf("header admitted or detailed error: %v", err)
				}
			})
		}
	}
	for key, value := range map[string]string{"Tailscale-User-Login": "alice@example.com", "Tailscale-User-Name": "Alice", "Tailscale-User-Profile-Pic": "https://example.test/pic", "Origin": mcpTestOrigin, "Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty", "Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": mcpProtocolVersion, "Content-Length": "1"} {
		for _, alias := range []bool{false, true} {
			t.Run(fmt.Sprintf("duplicate-%s/alias=%v", key, alias), func(t *testing.T) {
				r := mcpGuardRequest(t)
				r.Header.Set("Origin", mcpTestOrigin)
				r.Header.Set(key, value)
				if err := checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()); err != nil {
					t.Fatal(err)
				}
				if alias {
					r.Header[strings.ToLower(key)] = []string{value}
				} else {
					r.Header.Add(key, value)
				}
				if checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()) == nil {
					t.Fatal("duplicate admitted")
				}
			})
		}
	}
}

func TestMCPToolRequestMissingOrMalformedPrerequisites(t *testing.T) {
	r := mcpGuardRequest(t)
	for _, config := range []*MCPConfig{nil, {}, {Origin: mcpTestOrigin}, {Host: mcpTestConfig().Host}, {Origin: mcpTestOrigin + "/", Host: mcpTestConfig().Host}, {Origin: mcpTestOrigin, Host: "evil.example"}, {Origin: "http://Portal.tailnet.ts.net:8081", Host: "Portal.tailnet.ts.net:8081"}} {
		if checkMCPToolRequest(r, config, mcpTrusted()) == nil {
			t.Fatal("malformed config authorized")
		}
	}
	for _, trusted := range []*net.IPNet{nil, {}, {IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(32, 32)}} {
		if checkMCPToolRequest(r, mcpTestConfig(), trusted) == nil {
			t.Fatal("missing/changed trust authorized")
		}
	}
	if checkMCPToolRequest(nil, mcpTestConfig(), mcpTrusted()) == nil {
		t.Fatal("nil request authorized")
	}
	// A context identity is insufficient without the actual trusted source and raw headers.
	r.RemoteAddr = "192.0.2.1:1234"
	if checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()) == nil {
		t.Fatal("context alone authorized")
	}
}

type mcpGuardResponseWriter struct {
	*httptest.ResponseRecorder
	beforeWrite func()
}

func (w *mcpGuardResponseWriter) WriteHeader(status int) {
	w.beforeWrite()
	w.ResponseRecorder.WriteHeader(status)
}

func TestMCPToolRequestDispatchPreservesRequest(t *testing.T) {
	h := &mcpHandler{config: *mcpTestConfig(), slots: make(chan struct{}, 8)}
	r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"icons_list","arguments":{}}}`, true)
	var actual *http.Request
	var ctx context.Context
	var identity *Identity
	checked := false
	w := &mcpGuardResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	w.beforeWrite = func() {
		checked = true
		if actual == nil || actual.Context() != ctx || IdentityFromContext(ctx) != identity || len(h.slots) != 1 {
			t.Fatal("request/context/slot not preserved through response")
		}
		body := actual.Body
		for i := 0; i < 3; i++ {
			if err := checkMCPToolRequest(actual, mcpTestConfig(), mcpTrusted()); err != nil {
				t.Fatal(err)
			}
		}
		if actual.Body != body {
			t.Fatal("guard replaced consumed body")
		}
	}
	IdentityMiddleware(mcpTrusted(), http.HandlerFunc(func(w http.ResponseWriter, admitted *http.Request) {
		actual = admitted
		ctx = admitted.Context()
		identity = IdentityFromContext(ctx)
		h.ServeHTTP(w, admitted)
	})).ServeHTTP(w, r)
	if !checked || w.Code != 200 || len(h.slots) != 0 || !strings.Contains(w.Body.String(), `"structuredContent"`) {
		t.Fatal("dispatch response or slot cleanup changed")
	}
	if actual.URL != r.URL || actual.Header.Get("Tailscale-User-Login") != r.Header.Get("Tailscale-User-Login") {
		t.Fatal("request identity/URL changed")
	}
	// Direct dispatch takes this same middleware-created request and must never
	// reread even an exhausted body, nor replace its context/identity.
	body := &mcpGuardBody{}
	actual.Body = body
	out := httptest.NewRecorder()
	h.dispatch(out, actual, []byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}`), true)
	if out.Code != 200 || body.reads != 0 || body.closes != 0 || actual.Context() != ctx || IdentityFromContext(ctx) != identity {
		t.Fatal("dispatch consumed or substituted actual request")
	}
	for _, private := range []string{"alice@example.com", mcpTestOrigin, "Tailscale-User", "secret"} {
		if strings.Contains(w.Body.String(), private) || strings.Contains(out.Body.String(), private) {
			t.Fatal("private response data")
		}
	}
}

// No production callback/hook is justified solely to observe an unused argument.
// Verify the real route forwards its parameter directly, not a synthesized copy.
func TestMCPToolRequestDispatchForwardingSource(t *testing.T) {
	for _, name := range []string{"mcp_http.go", "mcp_protocol.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if name == "mcp_protocol.go" && fn.Name.Name == "dispatch" {
				found = true
				fields := fn.Type.Params.List
				if len(fields) != 4 || len(fields[1].Names) != 1 || fields[1].Names[0].Name != "r" {
					t.Fatal("dispatch lacks actual request parameter")
				}
				star, ok := fields[1].Type.(*ast.StarExpr)
				if !ok {
					t.Fatal("request not pointer")
				}
				sel, ok := star.X.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Request" {
					t.Fatal("request not http.Request")
				}
			}
			if name != "mcp_http.go" || fn.Name.Name != "ServeHTTP" {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if assignment, ok := node.(*ast.AssignStmt); ok {
					for _, lhs := range assignment.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name == "r" {
							t.Fatal("route replaces actual request")
						}
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "Clone" || sel.Sel.Name == "WithContext" {
					t.Fatal("route clones request before dispatch")
				}
				if sel.Sel.Name != "dispatch" {
					return true
				}
				if found {
					t.Fatal("unexpected duplicate dispatch")
				}
				found = true
				if len(call.Args) != 4 {
					t.Fatal("wrong dispatch shape")
				}
				id, ok := call.Args[1].(*ast.Ident)
				if !ok || id.Name != "r" {
					t.Fatal("dispatch not given actual r")
				}
				return true
			})
		}
		if !found {
			t.Fatal("missing dispatch definition/caller")
		}
	}
}

func TestMCPToolRequestSocketHeaders(t *testing.T) {
	// Real net/http parsing strips Transfer-Encoding into the request field. The
	// repeated guard must permit chunked bodies without trying to read them again.
	server := httptest.NewServer(IdentityMiddleware(mcpTrusted(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()) != nil {
			http.Error(w, "forbidden", 403)
			return
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if checkMCPToolRequest(r, mcpTestConfig(), mcpTrusted()) != nil {
			http.Error(w, "forbidden", 403)
			return
		}
		w.WriteHeader(204)
	})))
	defer server.Close()
	for _, test := range []struct {
		name, extra, framing, body string
		status                     int
	}{
		{"native", "", "Content-Length: 2\r\n", "{}", 204},
		{"browser", "Origin: " + mcpTestOrigin + "\r\nSec-Fetch-Mode: same-origin\r\n", "Content-Length: 2\r\n", "{}", 204},
		{"chunked", "", "Transfer-Encoding: chunked\r\n", "2\r\n{}\r\n0\r\n\r\n", 204},
		{"empty-auth", "Authorization:\r\n", "Content-Length: 0\r\n", "", 403},
		{"empty-cookie", "Cookie:\r\n", "Content-Length: 0\r\n", "", 403},
		{"empty-session", "Mcp-Session-Id:\r\n", "Content-Length: 0\r\n", "", 403},
		{"duplicate-login", "Tailscale-User-Login: alice@example.com\r\n", "Content-Length: 0\r\n", "", 403},
		{"duplicate-version", "MCP-Protocol-Version: " + mcpProtocolVersion + "\r\n", "Content-Length: 0\r\n", "", 403},
		{"metadata-without-origin", "Sec-Fetch-Site: same-origin\r\n", "Content-Length: 0\r\n", "", 403},
		{"duplicate-origin", "Origin: " + mcpTestOrigin + "\r\nOrigin: " + mcpTestOrigin + "\r\n", "Content-Length: 0\r\n", "", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			wire := fmt.Sprintf("POST /mcp HTTP/1.1\r\nHost: %s\r\nTailscale-User-Login: alice@example.com\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nMCP-Protocol-Version: %s\r\nConnection: close\r\n%s%s\r\n%s", mcpTestConfig().Host, mcpProtocolVersion, test.extra, test.framing, test.body)
			if _, err := io.WriteString(c, wire); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "POST"})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != test.status {
				t.Fatalf("status=%d err=%v", response.StatusCode, err)
			}
			if response.Header.Get("Access-Control-Allow-Origin") != "" || response.Header.Get("Mcp-Session-Id") != "" || strings.Contains(string(body), "alice") {
				t.Fatal("unexpected authority/CORS/private output")
			}
		})
	}
}
