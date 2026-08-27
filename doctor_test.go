package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	doctorTestAPIKey   = "fixture-api-secret"
	doctorTestPassword = "fixture-npm-password"
	doctorTestToken    = "fixture-jwt-secret"
)

type doctorScriptedControlPlane struct {
	provider controlPlaneProvider
	result   *ControlPlaneResult
}

func (d *doctorScriptedControlPlane) Provider() controlPlaneProvider { return d.provider }

func (d *doctorScriptedControlPlane) Load(_ context.Context, progress controlPlaneProgress) (*ControlPlaneResult, error) {
	if d.provider == controlPlaneTailscale {
		reportControlPlaneProgress(progress, controlPlaneStageAuth, 0)
		reportControlPlaneProgress(progress, controlPlaneStagePolicy, len(d.result.Policy.ACLs))
		reportControlPlaneProgress(progress, controlPlaneStageUsers, 2)
		reportControlPlaneProgress(progress, controlPlaneStageDevices, len(d.result.Nodes))
	}
	return d.result, nil
}

type doctorHTTPFixture struct {
	headscale *httptest.Server
	npm       *httptest.Server

	policyStatus int
	nodesStatus  int
	authStatus   int
	proxyStatus  int
	policyBody   string
	nodesBody    string
	authBody     string
	proxyBody    string

	policyHits atomic.Int32
	nodesHits  atomic.Int32
	authHits   atomic.Int32
	proxyHits  atomic.Int32
}

func newDoctorHTTPFixture(t *testing.T) *doctorHTTPFixture {
	return newDoctorHTTPFixtureWithHeadscaleTLS(t, true)
}

func newDoctorHTTPFixtureWithHeadscaleTLS(t *testing.T, useTLS bool) *doctorHTTPFixture {
	t.Helper()
	fixture := &doctorHTTPFixture{
		policyStatus: http.StatusOK,
		nodesStatus:  http.StatusOK,
		authStatus:   http.StatusOK,
		proxyStatus:  http.StatusOK,
		policyBody:   `{"policy":"{\"groups\":{\"group:admin\":[\"alice@example.com\"],\"group:dev\":[\"bob@example.com\"]},\"tagOwners\":{},\"hosts\":{},\"acls\":[{\"action\":\"accept\",\"src\":[\"group:admin\"],\"dst\":[\"10.0.0.1:*\"]},{\"action\":\"accept\",\"src\":[\"group:dev\"],\"dst\":[\"10.0.0.2:*\"]}]}","updatedAt":"2026-01-01T00:00:00Z"}`,
		nodesBody:    `{"nodes":[]}`,
		authBody:     `{"token":"` + doctorTestToken + `","expires":"` + futureExpiry() + `"}`,
		proxyBody: `[
			{"id":1,"domain_names":["admin.example.com"],"forward_scheme":"https","forward_host":"10.0.0.1","forward_port":443,"enabled":true,"meta":{"nginx_online":true}},
			{"id":2,"domain_names":["dev.example.com"],"forward_scheme":"http","forward_host":"10.0.0.2","forward_port":8080,"enabled":true,"meta":{"nginx_online":false}},
			{"id":3,"domain_names":["orphan.example.com"],"forward_scheme":"http","forward_host":"docker-orphan","forward_port":9000,"enabled":true,"meta":{"nginx_online":true}}
		]`,
	}

	headscaleMux := http.NewServeMux()
	headscaleMux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		fixture.policyHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+doctorTestAPIKey {
			t.Errorf("Headscale Authorization = %q", got)
		}
		writeDoctorFixtureResponse(w, fixture.policyStatus, fixture.policyBody)
	})
	headscaleMux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, r *http.Request) {
		fixture.nodesHits.Add(1)
		writeDoctorFixtureResponse(w, fixture.nodesStatus, fixture.nodesBody)
	})
	if useTLS {
		fixture.headscale = httptest.NewTLSServer(headscaleMux)
	} else {
		fixture.headscale = httptest.NewServer(headscaleMux)
	}
	t.Cleanup(fixture.headscale.Close)

	npmMux := http.NewServeMux()
	npmMux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		fixture.authHits.Add(1)
		writeDoctorFixtureResponse(w, fixture.authStatus, fixture.authBody)
	})
	npmMux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		fixture.proxyHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+doctorTestToken {
			t.Errorf("NPM Authorization = %q", got)
		}
		writeDoctorFixtureResponse(w, fixture.proxyStatus, fixture.proxyBody)
	})
	fixture.npm = httptest.NewServer(npmMux)
	t.Cleanup(fixture.npm.Close)
	return fixture
}

func writeDoctorFixtureResponse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func doctorFixtureConfig(fixture *doctorHTTPFixture) map[string]string {
	return map[string]string{
		"CONTROL_PLANE":      "headscale",
		"HEADSCALE_URL":      fixture.headscale.URL,
		"HEADSCALE_API_KEY":  doctorTestAPIKey,
		"NPM_URL":            fixture.npm.URL,
		"NPM_EMAIL":          "doctor@example.com",
		"NPM_PASSWORD":       doctorTestPassword,
		"LISTEN_ADDR":        "127.0.0.1:8080",
		"POLL_INTERVAL":      "30s",
		"TRUSTED_PROXY_CIDR": "127.0.0.1/32",
	}
}

func setDoctorProcessConfig(t *testing.T, values map[string]string) {
	t.Helper()
	t.Setenv(processEnvEncodingKey, "")
	t.Setenv("CONTROL_PLANE", values["CONTROL_PLANE"])
	keys := append(append([]string(nil), requiredConfigKeys...), tailscaleRequiredConfigKeys...)
	keys = append(keys, "LISTEN_ADDR", "POLL_INTERVAL")
	for _, key := range keys {
		t.Setenv(key, values[key])
	}
}

func doctorFixtureDependencies(fixture *doctorHTTPFixture) doctorDependencies {
	return doctorDependencies{newClients: func(cfg *Config) (ControlPlane, *NPMClient) {
		return NewHeadscaleClient(cfg.HeadscaleURL, cfg.HeadscaleAPIKey, fixture.headscale.Client()),
			NewNPMClient(cfg.NPMURL, cfg.NPMEmail, cfg.NPMPassword, fixture.npm.Client())
	}}
}

func runDoctorForTest(args []string, fixtures ...*doctorHTTPFixture) (int, string, string) {
	dependencies := defaultDoctorDependencies()
	if len(fixtures) > 0 {
		dependencies = doctorFixtureDependencies(fixtures[0])
	}
	var stdout, stderr bytes.Buffer
	code := runDoctorCommandWithDependencies(args, &stdout, &stderr, dependencies)
	return code, stdout.String(), stderr.String()
}

func TestRunDoctorCommandWarningsAndIdentityPreviews(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	values := doctorFixtureConfig(fixture)
	values["TRUSTED_PROXY_CIDR"] = "10.0.0.0/8"
	setDoctorProcessConfig(t, values)

	code, stdout, stderr := runDoctorForTest([]string{
		"--identity", "alice@example.com",
		"--identity", "bob@example.com",
	}, fixture)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, headscaleHTTPDoctorWarning) {
		t.Fatalf("HTTPS doctor emitted the Headscale HTTP warning:\n%s", stdout)
	}

	for _, want := range []string{
		"PASS config source: process environment",
		"PASS env file mode: not applicable",
		"PASS config: required values validated",
		"PASS control plane selection: headscale (explicit)",
		"WARN trusted proxy CIDR: 10.0.0.0/8",
		"PASS Headscale policy: loaded 2 ACL rules",
		"PASS Headscale nodes: loaded 0 nodes",
		"PASS NPM authentication: credentials accepted",
		"PASS NPM proxy hosts: loaded 3 proxy hosts",
		"PASS snapshot: complete (2 ACL rules, 0 nodes, 3 proxy hosts)",
		"PASS control plane metadata: provider=headscale policy_mode=legacy_acl_visibility_v1 support_level=supported",
		"WARN supported join coverage: 2/3 enabled proxy hosts",
		`WARN unmatched join: "orphan.example.com" -> "docker-orphan"`,
		`PASS identity preview "alice@example.com": 1 card`,
		`CARD "admin.example.com" -> "https://admin.example.com"`,
		`PASS identity preview "bob@example.com": 1 card`,
		`CARD "dev.example.com" -> "http://dev.example.com"`,
		"not proof of network authorization or reachability",
		"PASS doctor: required diagnostics completed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{doctorTestAPIKey, doctorTestPassword, doctorTestToken} {
		if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
			t.Errorf("doctor output exposed secret %q", secret)
		}
	}
}

func TestRunDoctorCommandReportsTailscalePreviewWithoutCredentialDisclosure(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	values := doctorFixtureConfig(fixture)
	values["CONTROL_PLANE"] = "tailscale"
	delete(values, "HEADSCALE_URL")
	delete(values, "HEADSCALE_API_KEY")
	values["TAILSCALE_OAUTH_CLIENT_ID"] = "private-oauth-client-id"
	values["TAILSCALE_OAUTH_CLIENT_SECRET"] = "private-oauth-client-secret"
	setDoctorProcessConfig(t, values)

	controlPlane := &doctorScriptedControlPlane{
		provider: controlPlaneTailscale,
		result: &ControlPlaneResult{
			Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.1:*"}}}},
			Metadata: ControlPlaneMetadata{
				Provider:     controlPlaneTailscale,
				PolicyMode:   legacyACLVisibilityV1,
				SupportLevel: controlPlanePreview,
			},
		},
	}
	dependencies := doctorDependencies{newClients: func(cfg *Config) (ControlPlane, *NPMClient) {
		return controlPlane, NewNPMClient(cfg.NPMURL, cfg.NPMEmail, cfg.NPMPassword, fixture.npm.Client())
	}}
	var stdout, stderr bytes.Buffer
	code := runDoctorCommandWithDependencies(nil, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"PASS control plane selection: tailscale (explicit)",
		"PASS Tailscale OAuth: access token acquired",
		"PASS Tailscale policy: loaded 1 ACL rules",
		"PASS Tailscale users: loaded 2 users",
		"PASS Tailscale devices: loaded 0 devices",
		"provider=tailscale policy_mode=legacy_acl_visibility_v1 support_level=preview",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), headscaleHTTPDoctorWarning) {
		t.Fatalf("Tailscale doctor emitted Headscale route warning: %q", stdout.String())
	}
	for _, forbidden := range []string{"private-oauth-client-id", "private-oauth-client-secret"} {
		if strings.Contains(stdout.String()+stderr.String(), forbidden) {
			t.Fatalf("doctor exposed %q", forbidden)
		}
	}
}

func TestRunDoctorCommandWarnsBeforeCallingHeadscaleOverHTTP(t *testing.T) {
	fixture := newDoctorHTTPFixtureWithHeadscaleTLS(t, false)
	setDoctorProcessConfig(t, doctorFixtureConfig(fixture))
	baseDependencies := doctorFixtureDependencies(fixture)
	var stdout, stderr bytes.Buffer
	dependencies := doctorDependencies{newClients: func(cfg *Config) (ControlPlane, *NPMClient) {
		if !strings.Contains(stdout.String(), headscaleHTTPDoctorWarning) {
			t.Error("Headscale HTTP warning was not written before upstream clients were created")
		}
		return baseDependencies.newClients(cfg)
	}}

	code := runDoctorCommandWithDependencies(nil, &stdout, &stderr, dependencies)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if count := strings.Count(stdout.String(), headscaleHTTPDoctorWarning); count != 1 {
		t.Fatalf("Headscale HTTP warning count = %d, stdout=%q", count, stdout.String())
	}
	if warningIndex, policyIndex := strings.Index(stdout.String(), headscaleHTTPDoctorWarning), strings.Index(stdout.String(), "PASS Headscale policy:"); warningIndex < 0 || policyIndex < 0 || warningIndex > policyIndex {
		t.Fatalf("Headscale HTTP warning did not precede upstream results:\n%s", stdout.String())
	}
	warningLine := ""
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, "WARN Headscale HTTP route:") {
			warningLine = line
			break
		}
	}
	for _, forbidden := range []string{fixture.headscale.URL, "127.0.0.1", fixture.headscale.Listener.Addr().String()} {
		if strings.Contains(warningLine, forbidden) {
			t.Fatalf("Headscale HTTP warning leaked route detail %q: %q", forbidden, warningLine)
		}
	}
}

func TestRunDoctorCommandRejectsMalformedRawComposeSecret(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	setDoctorProcessConfig(t, doctorFixtureConfig(fixture))
	secret := `"private-doctor-value\q"`
	t.Setenv("NPM_PASSWORD", secret)
	t.Setenv(processEnvEncodingKey, goQuotedEnvEncoding)

	code, stdout, stderr := runDoctorForTest(nil)
	if code != 1 || !strings.Contains(stdout, "FAIL config: loadConfig: invalid encoded environment value for NPM_PASSWORD") {
		t.Fatalf("exit code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "private-doctor-value") || strings.Contains(stderr, "private-doctor-value") {
		t.Fatalf("doctor exposed malformed secret: stdout=%q stderr=%q", stdout, stderr)
	}
	if fixture.policyHits.Load() != 0 || fixture.nodesHits.Load() != 0 || fixture.authHits.Load() != 0 || fixture.proxyHits.Load() != 0 {
		t.Fatal("doctor contacted upstreams after configuration decoding failed")
	}
}

func TestRunDoctorCommandWarnsForImplicitHeadscaleAndInactiveCredentials(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	values := doctorFixtureConfig(fixture)
	delete(values, "CONTROL_PLANE")
	values["TAILSCALE_OAUTH_CLIENT_ID"] = "inactive-client-id"
	values["TAILSCALE_OAUTH_CLIENT_SECRET"] = "inactive-client-secret"
	setDoctorProcessConfig(t, values)

	code, stdout, stderr := runDoctorForTest(nil, fixture)
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{
		"WARN control plane selection: " + implicitHeadscaleDeprecationMessage,
		"WARN inactive control-plane configuration: ignoring TAILSCALE_OAUTH_CLIENT_ID, TAILSCALE_OAUTH_CLIENT_SECRET",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{"inactive-client-id", "inactive-client-secret"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Fatalf("doctor exposed inactive credential %q", secret)
		}
	}
}

func TestRunDoctorCommandEnvironmentFileMode(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	values := doctorFixtureConfig(fixture)
	path := filepath.Join(t.TempDir(), "doctor.env")
	if err := writeEnvFile(path, values); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}

	ambient := cloneStrings(values)
	ambient["HEADSCALE_URL"] = "http://127.0.0.1:1"
	ambient["NPM_URL"] = "http://127.0.0.1:1"
	setDoctorProcessConfig(t, ambient)

	code, stdout, stderr := runDoctorForTest([]string{"--env-file", path}, fixture)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{
		"PASS config source: environment file",
		"PASS env file mode: owner-only permissions (0600)",
		"PASS doctor: required diagnostics completed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}

	before := fixture.policyHits.Load()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	code, stdout, stderr = runDoctorForTest([]string{"--env-file", path}, fixture)
	if code != 1 {
		t.Fatalf("unsafe mode exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "FAIL env file mode: permissions 0644") {
		t.Fatalf("stdout = %q", stdout)
	}
	if after := fixture.policyHits.Load(); after != before {
		t.Fatalf("policy endpoint called after unsafe file mode: before=%d after=%d", before, after)
	}
}

func TestRunDoctorCommandRequiredStageFailures(t *testing.T) {
	t.Run("config source", func(t *testing.T) {
		code, stdout, _ := runDoctorForTest([]string{"--env-file", filepath.Join(t.TempDir(), "missing.env")})
		if code != 1 || !strings.Contains(stdout, "FAIL config source:") {
			t.Fatalf("exit code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("config", func(t *testing.T) {
		setDoctorProcessConfig(t, map[string]string{})
		code, stdout, _ := runDoctorForTest(nil)
		if code != 1 || !strings.Contains(stdout, "FAIL config:") {
			t.Fatalf("exit code=%d stdout=%q", code, stdout)
		}
	})

	tests := []struct {
		name      string
		configure func(*doctorHTTPFixture)
		want      string
	}{
		{
			name: "Headscale policy",
			configure: func(fixture *doctorHTTPFixture) {
				fixture.policyStatus = http.StatusInternalServerError
				fixture.policyBody = `{"error":"policy unavailable"}`
			},
			want: "FAIL Headscale policy:",
		},
		{
			name: "Headscale nodes",
			configure: func(fixture *doctorHTTPFixture) {
				fixture.nodesStatus = http.StatusInternalServerError
				fixture.nodesBody = `{"error":"nodes unavailable"}`
			},
			want: "FAIL Headscale nodes:",
		},
		{
			name: "NPM authentication",
			configure: func(fixture *doctorHTTPFixture) {
				fixture.authStatus = http.StatusForbidden
				fixture.authBody = `{"error":"credentials rejected"}`
			},
			want: "FAIL NPM authentication:",
		},
		{
			name: "NPM proxy hosts",
			configure: func(fixture *doctorHTTPFixture) {
				fixture.proxyStatus = http.StatusInternalServerError
				fixture.proxyBody = `{"error":"proxy hosts unavailable"}`
			},
			want: "FAIL NPM proxy hosts:",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDoctorHTTPFixture(t)
			test.configure(fixture)
			setDoctorProcessConfig(t, doctorFixtureConfig(fixture))

			code, stdout, stderr := runDoctorForTest(nil, fixture)
			if code != 1 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.Contains(stdout, test.want) {
				t.Fatalf("stdout missing %q:\n%s", test.want, stdout)
			}
			if !strings.Contains(stdout, "FAIL snapshot: not created because") {
				t.Fatalf("snapshot failure was not reported:\n%s", stdout)
			}
			if strings.Contains(stdout, "PASS snapshot:") {
				t.Fatalf("snapshot passed after required stage failure:\n%s", stdout)
			}
		})
	}
}

func TestRunDoctorCommandRedactsAndBoundsErrors(t *testing.T) {
	t.Run("Headscale API key", func(t *testing.T) {
		fixture := newDoctorHTTPFixture(t)
		fixture.policyStatus = http.StatusInternalServerError
		fixture.policyBody = `{"error":"Bearer ` + doctorTestAPIKey + ` ` + strings.Repeat("x", 1000) + `"}`
		setDoctorProcessConfig(t, doctorFixtureConfig(fixture))

		code, stdout, _ := runDoctorForTest(nil, fixture)
		if code != 1 {
			t.Fatalf("exit code = %d, stdout=%q", code, stdout)
		}
		if strings.Contains(stdout, doctorTestAPIKey) {
			t.Fatalf("stdout exposed API key: %q", stdout)
		}
		if !strings.Contains(stdout, "[REDACTED]") {
			t.Fatalf("stdout did not mark redaction: %q", stdout)
		}
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, "FAIL Headscale policy:") && len([]rune(line)) > maxDoctorErrorLength+40 {
				t.Fatalf("failure line was not bounded: %d runes", len([]rune(line)))
			}
		}
	})

	t.Run("NPM token and password", func(t *testing.T) {
		fixture := newDoctorHTTPFixture(t)
		fixture.proxyStatus = http.StatusInternalServerError
		fixture.proxyBody = "{\"echo\":\"" + doctorTestToken + "\",\n\"password\":\"" + doctorTestPassword + "\"}"
		setDoctorProcessConfig(t, doctorFixtureConfig(fixture))

		code, stdout, _ := runDoctorForTest(nil, fixture)
		if code != 1 {
			t.Fatalf("exit code = %d, stdout=%q", code, stdout)
		}
		for _, secret := range []string{doctorTestToken, doctorTestPassword} {
			if strings.Contains(stdout, secret) {
				t.Fatalf("stdout exposed secret %q: %q", secret, stdout)
			}
		}
		if !strings.Contains(stdout, "[REDACTED]") {
			t.Fatalf("stdout did not mark redaction: %q", stdout)
		}
	})
}

func TestRunDoctorCommandUsage(t *testing.T) {
	code, stdout, stderr := runDoctorForTest([]string{"--help"})
	if code != 0 || !strings.Contains(stdout, "--identity LOGIN") || stderr != "" {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	for _, args := range [][]string{
		{"--identity", ""},
		{"--env-file"},
		{"positional"},
		{"--help", "--identity", "alice@example.com"},
	} {
		code, _, stderr = runDoctorForTest(args)
		if code != 2 || stderr == "" {
			t.Fatalf("args=%v exit=%d stderr=%q", args, code, stderr)
		}
	}
}
