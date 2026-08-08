package main

import (
	"bufio"
	"bytes"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func deterministicObservationPath(t *testing.T) string {
	t.Helper()
	path, err := newProxyObservationPath(bytes.NewReader(make([]byte, 24)))
	if err != nil {
		t.Fatalf("newProxyObservationPath() error = %v", err)
	}
	return path
}

func startObserverCommand(t *testing.T, path, confirmation string, timeout time.Duration) (net.Listener, <-chan int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	result := make(chan int, 1)
	dependencies := setupCommandDependencies{
		proxyObserver: proxyObserverDependencies{
			random: bytes.NewReader(make([]byte, 24)),
			listen: func(network, address string) (net.Listener, error) {
				if network != "tcp" {
					t.Errorf("listen network = %q, want tcp", network)
				}
				return listener, nil
			},
			shutdownTimeout: time.Second,
		},
	}
	go func() {
		result <- runSetupCommandWithDependencies(
			[]string{
				"observe-proxy",
				"--env-file", path,
				"--listen", "127.0.0.1:0",
				"--timeout", timeout.String(),
			},
			strings.NewReader(confirmation),
			stdout,
			stderr,
			dependencies,
		)
	}()
	return listener, result, stdout, stderr
}

func sendObservationRequest(t *testing.T, listener net.Listener, path string, headers http.Header) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+path, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header = headers.Clone()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("observation request error = %v", err)
	}
	return response
}

func awaitObserverResult(t *testing.T, result <-chan int) int {
	t.Helper()
	select {
	case code := <-result:
		return code
	case <-time.After(3 * time.Second):
		t.Fatal("observer command did not finish")
		return -1
	}
}

func TestProxyObserverConfirmedIPv4UpdatesOnlyTrustedProxyCIDR(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := validConfigValues()
	initial["HEADSCALE_API_KEY"] = "headscale-secret-never-print"
	initial["NPM_PASSWORD"] = "npm-secret-never-print"
	initial["LISTEN_ADDR"] = "0.0.0.0:8080"
	initial["POLL_INTERVAL"] = "30s"
	initial["TRUSTED_PROXY_CIDR"] = "203.0.113.50/32"
	initial["UNKNOWN_SETTING"] = "preserve"
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	listener, result, stdout, stderr := startObserverCommand(t, path, "yes\n", time.Second)
	headers := make(http.Header)
	headers.Set("Forwarded", "for=198.51.100.8")
	headers.Set("X-Forwarded-For", "198.51.100.9")
	headers.Set("X-Real-IP", "198.51.100.10")
	headers.Set("Tailscale-User-Login", "spoofed@example.com")
	response := sendObservationRequest(t, listener, deterministicObservationPath(t), headers)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %q", response.StatusCode, body)
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	if code := awaitObserverResult(t, result); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	got, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	want := cloneStrings(initial)
	want["TRUSTED_PROXY_CIDR"] = "127.0.0.1/32"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updated values = %#v, want only trusted CIDR changed to %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("file mode = %o, want 600", gotMode)
	}
	combined := stdout.String() + stderr.String()
	for _, secret := range []string{initial["HEADSCALE_API_KEY"], initial["NPM_PASSWORD"], "spoofed@example.com"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("observer output leaked %q: %q", secret, combined)
		}
	}
	if !strings.Contains(stdout.String(), "Observed immediate connection source: 127.0.0.1/32") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestProxyObservationHandlerIgnoresSpoofedHeadersAndUsesRemoteAddr(t *testing.T) {
	observed := make(chan string, 1)
	handler := &proxyObservationHandler{path: "/one-time", observed: observed}
	request := httptest.NewRequest(http.MethodGet, "http://observer/one-time", nil)
	request.RemoteAddr = "203.0.113.7:45678"
	request.Header.Set("Forwarded", "for=192.0.2.1")
	request.Header.Set("X-Forwarded-For", "192.0.2.2, 192.0.2.3")
	request.Header.Set("X-Real-IP", "192.0.2.4")
	request.Header.Set("Tailscale-User-Login", "mallory@example.com")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	select {
	case got := <-observed:
		if got != "203.0.113.7/32" {
			t.Fatalf("observed CIDR = %q, want RemoteAddr /32", got)
		}
	default:
		t.Fatal("handler did not report an observation")
	}
	for _, spoofed := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.4", "mallory@example.com"} {
		if strings.Contains(response.Body.String(), spoofed) {
			t.Fatalf("response included spoofed header value %q", spoofed)
		}
	}
}

func TestProxyObservationHandlerPathIsExactOneTimeAndNoStore(t *testing.T) {
	observed := make(chan string, 1)
	handler := &proxyObservationHandler{path: "/secret-path", observed: observed}

	wrong := httptest.NewRequest(http.MethodGet, "http://observer/not-secret", nil)
	wrong.RemoteAddr = "192.0.2.10:1234"
	wrongResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusNotFound {
		t.Fatalf("wrong path status = %d, want 404", wrongResponse.Code)
	}
	if !strings.Contains(wrongResponse.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("wrong path Cache-Control = %q", wrongResponse.Header().Get("Cache-Control"))
	}

	correct := httptest.NewRequest(http.MethodGet, "http://observer/secret-path", nil)
	correct.RemoteAddr = "192.0.2.10:1234"
	correctResponse := httptest.NewRecorder()
	handler.ServeHTTP(correctResponse, correct)
	if correctResponse.Code != http.StatusOK {
		t.Fatalf("correct path status = %d", correctResponse.Code)
	}

	reused := httptest.NewRequest(http.MethodGet, "http://observer/secret-path", nil)
	reused.RemoteAddr = "192.0.2.11:4321"
	reusedResponse := httptest.NewRecorder()
	handler.ServeHTTP(reusedResponse, reused)
	if reusedResponse.Code != http.StatusGone {
		t.Fatalf("reused path status = %d, want 410", reusedResponse.Code)
	}
	if !strings.Contains(reusedResponse.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("reused path Cache-Control = %q", reusedResponse.Header().Get("Cache-Control"))
	}
	if got := <-observed; got != "192.0.2.10/32" {
		t.Fatalf("first observation = %q", got)
	}
	select {
	case extra := <-observed:
		t.Fatalf("unexpected second observation %q", extra)
	default:
	}
}

func TestProxyObservationHandlerRejectsWrongMethodAndMalformedRemoteAddr(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		remoteAddr string
		wantStatus int
	}{
		{name: "method", method: http.MethodPost, remoteAddr: "192.0.2.1:80", wantStatus: http.StatusMethodNotAllowed},
		{name: "malformed remote", method: http.MethodGet, remoteAddr: "not-an-address", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := make(chan string, 1)
			handler := &proxyObservationHandler{path: "/observe", observed: observed}
			request := httptest.NewRequest(test.method, "http://observer/observe", nil)
			request.RemoteAddr = test.remoteAddr
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			select {
			case got := <-observed:
				t.Fatalf("unexpected observation %q", got)
			default:
			}
		})
	}
}

func TestExactCIDRFromRemoteAddrIPv4AndIPv6(t *testing.T) {
	tests := map[string]string{
		"192.0.2.25:443":            "192.0.2.25/32",
		"[2001:db8::25]:443":        "2001:db8::25/128",
		"[fe80::1234%eth0]:8080":    "fe80::1234/128",
		"[::ffff:192.0.2.33]:12345": "192.0.2.33/32",
	}
	for remoteAddr, want := range tests {
		t.Run(remoteAddr, func(t *testing.T) {
			got, err := exactCIDRFromRemoteAddr(remoteAddr)
			if err != nil {
				t.Fatalf("exactCIDRFromRemoteAddr() error = %v", err)
			}
			if got != want {
				t.Fatalf("exactCIDRFromRemoteAddr(%q) = %q, want %q", remoteAddr, got, want)
			}
		})
	}

	for _, remoteAddr := range []string{"", "192.0.2.1", "hostname:80", "[2001:db8::1]"} {
		t.Run("invalid_"+remoteAddr, func(t *testing.T) {
			if _, err := exactCIDRFromRemoteAddr(remoteAddr); err == nil {
				t.Fatalf("exactCIDRFromRemoteAddr(%q) error = nil", remoteAddr)
			}
		})
	}
}

func TestNewProxyObservationPathIsCryptoRandomURLSafeAndUnpredictable(t *testing.T) {
	first, err := newProxyObservationPath(cryptorand.Reader)
	if err != nil {
		t.Fatalf("first newProxyObservationPath() error = %v", err)
	}
	second, err := newProxyObservationPath(cryptorand.Reader)
	if err != nil {
		t.Fatalf("second newProxyObservationPath() error = %v", err)
	}
	pattern := regexp.MustCompile(`^/_velociportal/observe/[A-Za-z0-9_-]{32}$`)
	if !pattern.MatchString(first) || !pattern.MatchString(second) {
		t.Fatalf("paths are not URL-safe 192-bit tokens: %q %q", first, second)
	}
	if first == second {
		t.Fatalf("two crypto-random observation paths matched: %q", first)
	}
	if _, err := newProxyObservationPath(strings.NewReader("short")); err == nil {
		t.Fatal("newProxyObservationPath() accepted a short random source")
	}
	if _, err := newProxyObservationPath(nil); err == nil {
		t.Fatal("newProxyObservationPath(nil) error = nil")
	}
}

func TestProxyObserverRejectionLeavesFileByteForByteUnchanged(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := []byte("HEADSCALE_API_KEY=\"secret\"\nTRUSTED_PROXY_CIDR=\"198.51.100.1/32\"\nUNKNOWN=\"keep-order\"\n")
	if err := os.WriteFile(path, initial, 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	listener, result, stdout, stderr := startObserverCommand(t, path, "no\n", time.Second)
	response := sendObservationRequest(t, listener, deterministicObservationPath(t), make(http.Header))
	response.Body.Close()
	if code := awaitObserverResult(t, result); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(after, initial) {
		t.Fatalf("rejected update changed file: before=%q after=%q", initial, after)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("rejected update changed mode to %o", got)
	}
	if !strings.Contains(stdout.String(), "environment file was not changed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestProxyObserverRejectsWithoutParsingUpstreamSecrets(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	malformed := []byte("HEADSCALE_API_KEY=\"secret\"\nDUPLICATE=one\nDUPLICATE=two\n")
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	listener, result, stdout, stderr := startObserverCommand(t, path, "n\n", time.Second)
	response := sendObservationRequest(t, listener, deterministicObservationPath(t), make(http.Header))
	response.Body.Close()
	if code := awaitObserverResult(t, result); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "duplicate key") {
		t.Fatalf("observer parsed env file before confirmation: %q", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret") {
		t.Fatalf("observer leaked secret text: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(after, malformed) {
		t.Fatal("rejection changed malformed file")
	}
}

func TestProxyObserverTimeoutShutsDownWithoutChangingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := validConfigValues()
	initial["LISTEN_ADDR"] = "127.0.0.1:8080"
	initial["POLL_INTERVAL"] = "30s"
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	listener, result, stdout, stderr := startObserverCommand(t, path, "yes\n", 50*time.Millisecond)
	if code := awaitObserverResult(t, result); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "timed out") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("timeout changed environment file")
	}
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("observer listener still accepted connections after timeout")
	}
}

func TestProxyObserverRequiresExplicitValidConfirmation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := validConfigValues()
	initial["LISTEN_ADDR"] = "127.0.0.1:8080"
	initial["POLL_INTERVAL"] = "30s"
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}

	listener, result, stdout, stderr := startObserverCommand(t, path, "maybe\ny\n", time.Second)
	response := sendObservationRequest(t, listener, deterministicObservationPath(t), make(http.Header))
	response.Body.Close()
	if code := awaitObserverResult(t, result); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Enter yes or no") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Count(stdout.String(), "Update only TRUSTED_PROXY_CIDR") != 2 {
		t.Fatalf("unexpected confirmation output: %q", stdout.String())
	}
	values, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	if values["TRUSTED_PROXY_CIDR"] != "127.0.0.1/32" {
		t.Fatalf("TRUSTED_PROXY_CIDR = %q", values["TRUSTED_PROXY_CIDR"])
	}
}

func TestProxyObserverConfirmedMalformedEnvFileFailsWithoutReplacingIt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	malformed := []byte("DUPLICATE=one\nDUPLICATE=two\n")
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	listener, result, _, stderr := startObserverCommand(t, path, "yes\n", time.Second)
	response := sendObservationRequest(t, listener, deterministicObservationPath(t), make(http.Header))
	response.Body.Close()
	if code := awaitObserverResult(t, result); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "duplicate key") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(after, malformed) {
		t.Fatal("malformed env file was replaced after read error")
	}
}

func TestProxyObserverMissingEnvFileFailsBeforeListening(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	listenCalled := false
	dependencies := setupCommandDependencies{proxyObserver: proxyObserverDependencies{
		listen: func(string, string) (net.Listener, error) {
			listenCalled = true
			return nil, errors.New("must not listen")
		},
	}}
	var stdout, stderr bytes.Buffer
	code := runSetupCommandWithDependencies(
		[]string{"observe-proxy", "--env-file", missing, "--listen", "127.0.0.1:0", "--timeout", "1s"},
		strings.NewReader("yes\n"),
		&stdout,
		&stderr,
		dependencies,
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if listenCalled {
		t.Fatal("observer listened before checking the environment file")
	}
	if !strings.Contains(stderr.String(), "environment file") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestProxyObserverArgumentValidation(t *testing.T) {
	tests := [][]string{
		{"observe-proxy", "--listen", "example.com:8080"},
		{"observe-proxy", "--listen", "127.0.0.1"},
		{"observe-proxy", "--timeout", "0s"},
		{"observe-proxy", "--timeout", "not-a-duration"},
		{"observe-proxy", "--env-file="},
		{"observe-proxy", "extra"},
		{"observe-proxy", "--help", "extra"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runSetupCommand(args, strings.NewReader(""), &stdout, &stderr)
			if code != 2 {
				t.Fatalf("args %v exit code = %d, want 2; stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestNormalizeProxyObserverListenAddrAllowsEphemeralPortOnly(t *testing.T) {
	for input, want := range map[string]string{
		"127.0.0.1:0": "127.0.0.1:0",
		"[::1]:0":     "[::1]:0",
		"localhost:0": "localhost:0",
	} {
		got, err := normalizeProxyObserverListenAddr(input)
		if err != nil {
			t.Fatalf("normalizeProxyObserverListenAddr(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeProxyObserverListenAddr(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"example.com:0", ":0", "224.0.0.1:0", "127.0.0.1:-1"} {
		if _, err := normalizeProxyObserverListenAddr(input); err == nil {
			t.Fatalf("normalizeProxyObserverListenAddr(%q) error = nil", input)
		}
	}
}

func TestConfirmProxyCIDRRejectsEOFWithoutApproval(t *testing.T) {
	var stdout, stderr bytes.Buffer
	confirmed, err := confirmProxyCIDR(bufioReader(""), &stdout, &stderr, ".env", "192.0.2.1/32")
	if err == nil || confirmed {
		t.Fatalf("confirmed = %v, error = %v", confirmed, err)
	}
}

func bufioReader(value string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(value))
}

func TestEnsureProxyObserverEnvFileExists(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.env")
	if err := ensureProxyObserverEnvFileExists(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing error = %v", err)
	}
	directory := t.TempDir()
	if err := ensureProxyObserverEnvFileExists(directory); err == nil {
		t.Fatal("directory accepted as environment file")
	}
}
