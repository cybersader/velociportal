package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mcpTrusted() *net.IPNet { _, network, _ := net.ParseCIDR("127.0.0.1/32"); return network }
func mcpTestConfig() *MCPConfig {
	host, _ := validatePortalPublicOrigin(mcpTestOrigin)
	return &MCPConfig{Origin: mcpTestOrigin, Host: host}
}
func mcpTestApplicationTools() *mcpApplicationTools {
	return newMCPApplicationTools(&Config{MCP: mcpTestConfig(), TrustedProxyCIDR: mcpTrusted()}, nil, nil, nil)
}

func mcpTestRequest(body string, version bool) *http.Request {
	r := httptest.NewRequest("POST", mcpTestOrigin+"/mcp", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Tailscale-User-Login", "alice@example.com")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if version {
		r.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	}
	return r
}
func mcpTestServe(t *testing.T, handler http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Mcp-Session-Id") != "" {
		t.Fatalf("unsafe response headers: %v", w.Header())
	}
	return w
}

func TestMCPHTTPAdmission(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, test := range []struct {
		name   string
		status int
		change func(*http.Request)
	}{
		{"native", 200, func(r *http.Request) {}},
		{"browser", 200, func(r *http.Request) {
			r.Header.Set("Origin", mcpTestOrigin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Sec-Fetch-Dest", "empty")
		}},
		{"origin-only", 200, func(r *http.Request) { r.Header.Set("Origin", mcpTestOrigin) }},
		{"metadata-only", 403, func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "same-origin")
		}},
		{"site-without-origin", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }},
		{"mode-without-origin", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "cors") }},
		{"dest-without-origin", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "empty") }},
		{"browser-site-cross", 403, func(r *http.Request) {
			r.Header.Set("Origin", mcpTestOrigin)
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}},
		{"browser-mode-navigation", 403, func(r *http.Request) {
			r.Header.Set("Origin", mcpTestOrigin)
			r.Header.Set("Sec-Fetch-Mode", "navigate")
		}},
		{"browser-dest-document", 403, func(r *http.Request) {
			r.Header.Set("Origin", mcpTestOrigin)
			r.Header.Set("Sec-Fetch-Dest", "document")
		}},
		{"browser-fetch-user", 403, func(r *http.Request) { r.Header.Set("Origin", mcpTestOrigin); r.Header.Set("Sec-Fetch-User", "?1") }},
		{"untrusted", 403, func(r *http.Request) { r.RemoteAddr = "192.0.2.1:12345" }},
		{"identity-missing", 401, func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }},
		{"identity-padded", 403, func(r *http.Request) { r.Header.Set("Tailscale-User-Login", " alice@example.com") }},
		{"identity-tagged", 403, func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "tag:server") }},
		{"identity-combined", 403, func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "alice,bob@example.com") }},
		{"identity-short", 403, func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "alice") }},
		{"host", 403, func(r *http.Request) { r.Host = "evil.example" }},
		{"host-case", 403, func(r *http.Request) { r.Host = "Portal.tailnet.ts.net:8081" }},
		{"query", 404, func(r *http.Request) { r.URL.RawQuery = "x=1" }},
		{"force-query", 404, func(r *http.Request) { r.URL.ForceQuery = true }},
		{"encoded-path", 404, func(r *http.Request) { r.URL.RawPath = "/%6dcp" }},
		{"trailing-slash", 404, func(r *http.Request) { r.URL.Path = "/mcp/" }},
		{"path", 404, func(r *http.Request) { r.URL.Path = "/other" }},
		{"origin-null", 403, func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{"origin-empty", 403, func(r *http.Request) { r.Header.Set("Origin", "") }},
		{"origin-cross", 403, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }},
		{"site-none", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }},
		{"site-cross", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"mode-navigation", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }},
		{"mode-nocors", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "no-cors") }},
		{"dest-document", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "document") }},
		{"fetch-user", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-User", "?1") }},
		{"preflight", 403, func(r *http.Request) { r.Method = "OPTIONS"; r.Header.Set("Access-Control-Request-Method", "POST") }},
		{"form", 415, func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }},
		{"charset", 200, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }},
		{"content-type-parameter", 415, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; foo=bar") }},
		{"accept-json-only", 406, func(r *http.Request) { r.Header.Set("Accept", "application/json") }},
		{"accept-missing", 406, func(r *http.Request) { r.Header.Del("Accept") }},
		{"accept-q-zero", 406, func(r *http.Request) { r.Header.Set("Accept", "application/json;q=0, text/event-stream") }},
		{"unsupported-version", 400, func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-03-26") }},
		{"missing-version", 400, func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") }},
		{"empty-version", 400, func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "") }},
		{"compression", 400, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"trailer", 400, func(r *http.Request) { r.Trailer = http.Header{"X-Trailer": []string{"value"}} }},
		{"transfer-conflict", 400, func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
		{"content-length-combined", 400, func(r *http.Request) { r.Header.Set("Content-Length", "1, 2") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, true)
			test.change(r)
			w := mcpTestServe(t, handler, r)
			if w.Code != test.status {
				t.Fatalf("got %d want %d: %s", w.Code, test.status, w.Body.String())
			}
		})
	}
	for _, key := range []string{"Authorization", "Cookie", "Mcp-Session-Id", "Sec-Fetch-User"} {
		for _, value := range []string{"", "value"} {
			r := mcpTestRequest(`{}`, true)
			r.Header.Set(key, value)
			if w := mcpTestServe(t, handler, r); w.Code != 403 {
				t.Fatalf("accepted unsupported %s=%q", key, value)
			}
		}
	}
	for _, key := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic", "Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Content-Type", "Accept", "MCP-Protocol-Version", "Content-Length"} {
		t.Run("duplicate-"+key, func(t *testing.T) {
			for _, mixedCase := range []bool{false, true} {
				r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, true)
				value := r.Header.Get(key)
				if value == "" {
					value = "same-origin"
				}
				r.Header.Set(key, value)
				if mixedCase {
					r.Header[strings.ToLower(key)] = []string{value}
				} else {
					r.Header.Add(key, value)
				}
				w := mcpTestServe(t, handler, r)
				if w.Code < 400 {
					t.Fatalf("accepted duplicate %s mixed=%v", key, mixedCase)
				}
			}
		})
	}
	for _, method := range []string{"GET", "HEAD", "DELETE", "PUT", "OPTIONS", "PATCH"} {
		r := mcpTestRequest("", false)
		r.Method = method
		w := mcpTestServe(t, handler, r)
		if w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Fatalf("method %s got %d", method, w.Code)
		}
		r.Header.Del("Tailscale-User-Login")
		if w := mcpTestServe(t, handler, r); w.Code != 401 {
			t.Fatal("method rejection preceded authentication")
		}
	}
}

func TestMCPBodyBoundsAndFraming(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, streaming := range []bool{false, true} {
		for _, size := range []int{mcpMaxRequestBytes, mcpMaxRequestBytes + 1} {
			body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
			body += strings.Repeat(" ", size-len(body))
			r := mcpTestRequest(body, true)
			if streaming {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			w := mcpTestServe(t, handler, r)
			want := 200
			if size > mcpMaxRequestBytes {
				want = 413
			}
			if w.Code != want {
				t.Fatalf("size=%d streaming=%v got %d", size, streaming, w.Code)
			}
		}
	}
	for _, body := range []string{`{} {}`, `{"jsonrpc":`, "\xff"} {
		w := mcpTestServe(t, handler, mcpTestRequest(body, true))
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":-32700`) {
			t.Fatalf("framing: %d %s", w.Code, w.Body.String())
		}
	}
}

type mcpBlockingBody struct {
	entered chan struct{}
	release chan struct{}
	body    io.Reader
}

func (b *mcpBlockingBody) Read(p []byte) (int, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.body.Read(p)
}
func (b *mcpBlockingBody) Close() error { return nil }

func TestMCPAdmissionSlots(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 8)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	for i := 0; i < 8; i++ {
		r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, true)
		r.Body = &mcpBlockingBody{entered, release, r.Body}
		go func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, r); done <- w }()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("slots did not fill")
		}
	}
	if w := mcpTestServe(t, handler, mcpTestRequest(`{}`, true)); w.Code != 503 {
		t.Fatalf("ninth request got %d", w.Code)
	}
	close(release)
	for i := 0; i < 8; i++ {
		select {
		case w := <-done:
			if w.Code != 200 {
				t.Fatalf("admitted got %d", w.Code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("slot did not release")
		}
	}
	if w := mcpTestServe(t, handler, mcpTestRequest(`{"jsonrpc":"2.0","id":2,"method":"ping"}`, true)); w.Code != 200 {
		t.Fatal("slot leak")
	}
}

type mcpDeadlineWriter struct {
	*httptest.ResponseRecorder
	events []string
}

func (w *mcpDeadlineWriter) SetReadDeadline(value time.Time) error {
	event := "read-set"
	if value.IsZero() {
		event = "read-clear"
	}
	w.events = append(w.events, event)
	return nil
}
func (w *mcpDeadlineWriter) SetWriteDeadline(value time.Time) error {
	event := "write-set"
	if value.IsZero() {
		event = "write-clear"
	}
	w.events = append(w.events, event)
	return nil
}
func (w *mcpDeadlineWriter) FlushError() error { w.events = append(w.events, "flush"); return nil }

type mcpTimeoutBody struct{}

func (mcpTimeoutBody) Read([]byte) (int, error) { return 0, &net.DNSError{IsTimeout: true} }
func (mcpTimeoutBody) Close() error             { return nil }

func TestMCPRouteDeadlines(t *testing.T) {
	handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, test := range []struct {
		name   string
		status int
		change func(*http.Request)
	}{
		{"valid", 200, func(r *http.Request) {}},
		{"unauthenticated", 401, func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }},
		{"slow-body", 408, func(r *http.Request) { r.Body = mcpTimeoutBody{} }},
	} {
		r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, true)
		test.change(r)
		w := &mcpDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		handler.ServeHTTP(w, r)
		wantEvents := "read-set,write-set,flush,write-clear,read-clear"
		if test.status != 200 {
			wantEvents = "read-set,write-set,read-set,flush,write-clear,read-clear"
		}
		if w.Code != test.status || strings.Join(w.events, ",") != wantEvents {
			t.Fatalf("%s status=%d events=%v", test.name, w.Code, w.events)
		}
		if test.status != 200 && w.Header().Get("Connection") != "close" {
			t.Fatal("unread body could drain outside deadline")
		}
	}
}

func TestMCPResponseBound(t *testing.T) {
	w := httptest.NewRecorder()
	mcpWrite(w, 200, jsonRawID(), map[string]string{"value": string(bytes.Repeat([]byte{'x'}, mcpMaxResponseBytes))}, 0, "")
	if w.Code != 500 || w.Body.Len() > mcpMaxResponseBytes || !strings.Contains(w.Body.String(), `"code":-32603`) {
		t.Fatalf("response bound: %d %d", w.Code, w.Body.Len())
	}
}
func jsonRawID() []byte { return []byte(`1`) }

// Observe entry into the actual socket-backed body reader without replacing its
// Close behavior: net/http's cleanup, not a recorder, is under test below.
type mcpObservedSocketBody struct {
	io.ReadCloser
	entered  chan<- struct{}
	notified bool
}

func (b *mcpObservedSocketBody) Read(p []byte) (int, error) {
	if !b.notified {
		b.notified = true
		b.entered <- struct{}{}
	}
	return b.ReadCloser.Read(p)
}

func TestMCPRejectedSocketBodyCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		identity  bool
		origin    string
		saturated bool
		status    int
	}{
		{"identity", false, "", false, http.StatusUnauthorized},
		{"origin", true, "http://evil.example", false, http.StatusForbidden},
		{"slots", true, "", true, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			entered := make(chan struct{}, 16)
			closed := make(chan string, 32)
			handler := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body = &mcpObservedSocketBody{ReadCloser: r.Body, entered: entered}
				handler.ServeHTTP(w, r)
			}))
			server.Config.ReadHeaderTimeout = time.Second
			server.Config.ConnState = func(c net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					closed <- c.RemoteAddr().String()
				}
			}
			server.Start()
			var connections []net.Conn
			defer func() {
				for _, c := range connections {
					_ = c.Close()
				}
				server.Close()
			}()
			open := func(identity bool, origin string) net.Conn {
				t.Helper()
				c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				connections = append(connections, c)
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				headers := fmt.Sprintf("POST /mcp HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nMCP-Protocol-Version: %s\r\nContent-Length: 10\r\n", mcpTestConfig().Host, mcpProtocolVersion)
				if identity {
					headers += "Tailscale-User-Login: alice@example.com\r\n"
				}
				if origin != "" {
					headers += "Origin: " + origin + "\r\n"
				}
				// No Connection: close request header and no body: a small unread
				// keepalive body would otherwise be drained after handler return.
				if _, err := io.WriteString(c, headers+"\r\n"); err != nil {
					t.Fatal(err)
				}
				return c
			}
			if test.saturated {
				for i := 0; i < 8; i++ {
					open(true, "")
				}
				for i := 0; i < 8; i++ {
					select {
					case <-entered:
					case <-time.After(3 * time.Second):
						t.Fatal("admission slots did not fill")
					}
				}
			}
			c := open(test.identity, test.origin)
			response, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatalf("rejection response blocked: %v", err)
			}
			if response.StatusCode != test.status {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.status)
			}
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatalf("rejection body blocked: %v", err)
			}
			// Receipt of a response is insufficient: verify server connection
			// cleanup completes while the client keeps its request body withheld.
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			for {
				select {
				case address := <-closed:
					if address == c.LocalAddr().String() {
						return
					}
				case <-timer.C:
					t.Fatal("server retained rejected connection during body cleanup")
				}
			}
		})
	}
}

func TestMCPSocketKeepAlive(t *testing.T) {
	server := httptest.NewServer(newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools()))
	defer server.Close()
	c, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(c)
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, i+1)
		request := mcpTestRequest(body, true)
		if err := request.Write(c); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, request)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || response.Close {
			t.Fatalf("request %d: status=%d close=%v err=%v", i, response.StatusCode, response.Close, readErr)
		}
	}
}
