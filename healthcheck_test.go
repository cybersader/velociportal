package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthcheckDefaults(t *testing.T) {
	if defaultHealthcheckURL != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("defaultHealthcheckURL = %q", defaultHealthcheckURL)
	}
	if defaultHealthcheckTimeout != 3*time.Second {
		t.Fatalf("defaultHealthcheckTimeout = %s", defaultHealthcheckTimeout)
	}
}

func TestRunHealthcheckCommandAcceptsOnlyHTTP200(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantCode   int
	}{
		{name: "ok", statusCode: http.StatusOK, wantCode: 0},
		{name: "no content", statusCode: http.StatusNoContent, wantCode: 1},
		{name: "service unavailable", statusCode: http.StatusServiceUnavailable, wantCode: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if authorization := r.Header.Get("Authorization"); authorization != "" {
					t.Errorf("Authorization = %q, want empty", authorization)
				}
				w.WriteHeader(test.statusCode)
			}))
			defer server.Close()

			t.Setenv("HEADSCALE_API_KEY", "must-not-be-used")
			t.Setenv("NPM_PASSWORD", "must-not-be-used")

			var stdout, stderr bytes.Buffer
			code := runHealthcheckCommand([]string{"--url", server.URL}, &stdout, &stderr)
			if code != test.wantCode {
				t.Fatalf("exit code = %d, want %d; stdout=%q stderr=%q", code, test.wantCode, stdout.String(), stderr.String())
			}
			if test.wantCode == 0 {
				if stdout.String() != "healthy\n" || stderr.Len() != 0 {
					t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				return
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), fmt.Sprintf("HTTP %d", test.statusCode)) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunHealthcheckCommandDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/healthy", http.StatusFound)
		case "/healthy":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := runHealthcheckCommand([]string{"--url", server.URL + "/redirect"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 302") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHealthcheckCommandUsesTotalTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	started := time.Now()
	var stdout, stderr bytes.Buffer
	code := runHealthcheckCommand([]string{"--url", server.URL, "--timeout", "20ms"}, &stdout, &stderr)
	elapsed := time.Since(started)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if elapsed >= 90*time.Millisecond {
		t.Fatalf("healthcheck took %s, expected timeout before handler response", elapsed)
	}
	if !strings.Contains(stderr.String(), "deadline exceeded") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHealthcheckCommandDoesNotConsumeUnboundedBody(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
	}))

	started := time.Now()
	var stdout, stderr bytes.Buffer
	code := runHealthcheckCommand([]string{"--url", server.URL, "--timeout", "250ms"}, &stdout, &stderr)
	elapsed := time.Since(started)
	close(release)
	server.Close()

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("healthcheck waited for response body: %s", elapsed)
	}
}

func TestRunHealthcheckCommandBoundsResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Oversized", strings.Repeat("x", maxHealthcheckHeaders+1024))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := runHealthcheckCommand([]string{"--url", server.URL}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "response headers exceeded") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunHealthcheckCommandUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--wat"}},
		{name: "missing URL value", args: []string{"--url"}},
		{name: "empty URL", args: []string{"--url="}},
		{name: "relative URL", args: []string{"--url", "/healthz"}},
		{name: "unsupported scheme", args: []string{"--url", "file:///healthz"}},
		{name: "credentials", args: []string{"--url", "http://user:pass@example.com/healthz"}},
		{name: "empty port", args: []string{"--url", "http://example.com:/healthz"}},
		{name: "out of range port", args: []string{"--url", "http://example.com:70000/healthz"}},
		{name: "fragment", args: []string{"--url", "http://example.com/healthz#secret"}},
		{name: "invalid timeout", args: []string{"--timeout", "soon"}},
		{name: "zero timeout", args: []string{"--timeout", "0s"}},
		{name: "negative timeout", args: []string{"--timeout", "-1s"}},
		{name: "positional argument", args: []string{"extra"}},
		{name: "help with argument", args: []string{"--help", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runHealthcheckCommand(test.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("expected usage error on stderr")
			}
		})
	}
}

func TestRunHealthcheckCommandHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runHealthcheckCommand([]string{arg}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "--url URL") || !strings.Contains(stdout.String(), "--timeout DURATION") {
				t.Fatalf("stdout = %q", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}
