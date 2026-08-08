package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func setValidationProcessConfig(t *testing.T) {
	t.Helper()
	t.Setenv(processEnvEncodingKey, "")
	for key, value := range validConfigValues() {
		t.Setenv(key, value)
	}
	t.Setenv("LISTEN_ADDR", "127.0.0.1:8080")
	t.Setenv("POLL_INTERVAL", "30s")
}

func setValidationBuildInfo(t *testing.T, version, revision, state string) {
	t.Helper()
	originalVersion, originalRevision, originalState := buildVersion, buildRevision, buildSourceState
	t.Cleanup(func() {
		buildVersion, buildRevision, buildSourceState = originalVersion, originalRevision, originalState
	})
	buildVersion, buildRevision, buildSourceState = version, revision, state
}

func validationTestSnapshot() *CacheData {
	return &CacheData{
		Policy: &Policy{
			Groups: map[string][]string{
				"group:alpha": {"alice@example.com"},
				"group:beta":  {"bob@example.com"},
			},
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"group:alpha"}, Dst: []string{"10.0.0.10:*"}},
				{Action: "accept", Src: []string{"group:beta"}, Dst: []string{"tag:beta:*"}},
			},
		},
		Nodes: []Node{{User: User{Name: "service@example.com"}, Tags: []string{"tag:beta"}, IPAddresses: []string{"10.0.0.20"}}},
		ProxyHosts: []ProxyHost{
			{ID: 20, DomainNames: []string{"beta.internal.example"}, ForwardScheme: "http", ForwardHost: "10.0.0.20", ForwardPort: 8080, Enabled: true},
			{ID: 10, DomainNames: []string{"alpha.internal.example"}, ForwardScheme: "https", ForwardHost: "10.0.0.10", ForwardPort: 443, Enabled: true},
		},
	}
}

func validationDependenciesFor(snapshot *CacheData) validationDependencies {
	return validationDependencies{
		now: func() time.Time { return time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC) },
		loadSnapshot: func(context.Context, *HeadscaleClient, *NPMClient) (*CacheData, error) {
			return snapshot, nil
		},
	}
}

func runValidationForTest(args []string, dependencies validationDependencies) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runValidationCommandWithDependencies(args, &stdout, &stderr, dependencies)
	return code, stdout.String(), stderr.String()
}

func TestRunValidationCommandSummaryJSONIsDeterministicAndPrivate(t *testing.T) {
	setValidationProcessConfig(t)
	setValidationBuildInfo(t, "v1.2.3", "revision-canary", "clean")
	snapshot := validationTestSnapshot()
	args := []string{
		"--identity", "alpha=alice@example.com",
		"--identity", "beta=bob@example.com",
		"--format", "json",
	}
	firstCode, first, firstErr := runValidationForTest(args, validationDependenciesFor(snapshot))
	secondCode, second, secondErr := runValidationForTest(args, validationDependenciesFor(snapshot))
	if firstCode != 0 || secondCode != 0 {
		t.Fatalf("exit codes = %d, %d; stderr=%q %q", firstCode, secondCode, firstErr, secondErr)
	}
	if first != second {
		t.Fatalf("JSON output was not deterministic:\n%s\n%s", first, second)
	}
	for _, sensitive := range []string{
		"alice@example.com",
		"bob@example.com",
		"alpha.internal.example",
		"beta.internal.example",
		"10.0.0.10",
		"10.0.0.20",
		"backend-beta",
		"test-key",
		"changeme",
	} {
		if strings.Contains(first, sensitive) || strings.Contains(firstErr, sensitive) {
			t.Fatalf("summary output exposed %q:\nstdout=%s\nstderr=%s", sensitive, first, firstErr)
		}
	}

	var report ValidationReport
	if err := json.Unmarshal([]byte(first), &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if report.SchemaVersion != validationSchemaVersion || report.Status != "pass" || report.Privacy != string(validationPrivacySummary) {
		t.Fatalf("report metadata = %#v", report)
	}
	if len(report.Services) != 2 || report.Services[0].ID != "service-001" || report.Services[1].ID != "service-002" {
		t.Fatalf("services = %#v", report.Services)
	}
	if report.Services[0].Private != nil || report.Identities[0].Services[0].Private != nil {
		t.Fatal("summary report included private fields")
	}
	if len(report.Identities) != 2 || report.Identities[0].Label != "alpha" || report.Identities[1].Label != "beta" {
		t.Fatalf("identities = %#v", report.Identities)
	}
	if len(report.CommonServices) != 0 {
		t.Fatalf("common services = %v", report.CommonServices)
	}
}

func TestRunValidationCommandPrivateReportExplainsMatchesWithoutLogins(t *testing.T) {
	setValidationProcessConfig(t)
	setValidationBuildInfo(t, "v1.2.3", "revision", "clean")
	code, stdout, stderr := runValidationForTest([]string{
		"--identity", "alpha=alice@example.com",
		"--identity", "beta=bob@example.com",
		"--format", "json",
		"--privacy", "private",
	}, validationDependenciesFor(validationTestSnapshot()))
	if code != 0 {
		t.Fatalf("exit code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "contains internal topology") {
		t.Fatalf("stderr did not include private warning: %q", stderr)
	}
	for _, want := range []string{"alpha.internal.example", "10.0.0.10", "group:alpha", "tag:beta"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("private report missing %q:\n%s", want, stdout)
		}
	}
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		if strings.Contains(stdout, login) || strings.Contains(stderr, login) {
			t.Fatalf("private output exposed login %q", login)
		}
	}
}

func TestRunValidationCommandReviewFindingsExitOne(t *testing.T) {
	setValidationProcessConfig(t)
	setValidationBuildInfo(t, "dev", unknownBuildValue, "dirty")
	snapshot := validationTestSnapshot()
	snapshot.ProxyHosts = append(snapshot.ProxyHosts, ProxyHost{ID: 30, DomainNames: []string{"orphan.internal.example"}, ForwardHost: "unmatched-backend", Enabled: true})

	code, stdout, stderr := runValidationForTest([]string{
		"--identity", "alpha=alice@example.com",
		"--identity", "beta=bob@example.com",
		"--format", "json",
	}, validationDependenciesFor(snapshot))
	if code != 1 || stderr != "" {
		t.Fatalf("exit code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, code := range []string{"untraceable-build", "unmatched-forward-host"} {
		if !strings.Contains(stdout, `"code": "`+code+`"`) {
			t.Fatalf("report missing finding %q:\n%s", code, stdout)
		}
	}
}

func TestRunValidationCommandZeroCardSetRequiresReview(t *testing.T) {
	setValidationProcessConfig(t)
	setValidationBuildInfo(t, "v1", "revision", "clean")
	snapshot := validationTestSnapshot()
	snapshot.Policy.Groups["group:beta"] = []string{"alice@example.com"}

	code, stdout, _ := runValidationForTest([]string{
		"--identity", "alpha=alice@example.com",
		"--identity", "beta=bob@example.com",
		"--format", "json",
	}, validationDependenciesFor(snapshot))
	if code != 1 {
		t.Fatalf("exit code=%d stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"code": "zero-card-identity"`) {
		t.Fatalf("report missing zero-card finding:\n%s", stdout)
	}
}

func TestRunValidationCommandIdenticalCardSetsRequireReview(t *testing.T) {
	setValidationProcessConfig(t)
	setValidationBuildInfo(t, "v1", "revision", "clean")
	snapshot := validationTestSnapshot()
	snapshot.Policy = &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.0/24:*"}}}}

	code, stdout, _ := runValidationForTest([]string{
		"--identity", "alpha=alice@example.com",
		"--identity", "beta=bob@example.com",
		"--format", "json",
	}, validationDependenciesFor(snapshot))
	if code != 1 {
		t.Fatalf("exit code=%d stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"code": "identical-card-sets"`) {
		t.Fatalf("report missing identical-card finding:\n%s", stdout)
	}
}

func TestRunValidationCommandUsage(t *testing.T) {
	if code, stdout, stderr := runValidationForTest([]string{"--help"}, validationDependencies{}); code != 0 || !strings.Contains(stdout, "LABEL=LOGIN") || stderr != "" {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	for _, args := range [][]string{
		nil,
		{"--identity", "one=alice@example.com"},
		{"--identity", "bad"},
		{"--identity", "bad label=alice@example.com", "--identity", "two=bob@example.com"},
		{"--identity", "one=alice@example.com", "--identity", "one=bob@example.com"},
		{"--identity", "one=alice@example.com", "--identity", "two=alice@example.com"},
		{"--identity", "one=alice@example.com", "--identity", "two=bob@example.com", "--format", "yaml"},
		{"--identity", "one=alice@example.com", "--identity", "two=bob@example.com", "--privacy", "public"},
		{"--help", "--identity", "one=alice@example.com"},
		{"positional"},
	} {
		code, _, stderr := runValidationForTest(args, validationDependencies{})
		if code != 2 || stderr == "" {
			t.Fatalf("args=%v exit=%d stderr=%q", args, code, stderr)
		}
	}
}

func TestRunValidationCommandUsageDoesNotEchoRejectedLogins(t *testing.T) {
	for _, args := range [][]string{
		{"--identity", "bad label=login-canary@example.com"},
		{"--identity", "one=login-canary@example.com", "--identity", "two=login-canary@example.com"},
		{"--identity", "one=valid@example.com", "--identity", "two=login-canary@example.com\nforged"},
	} {
		code, stdout, stderr := runValidationForTest(args, validationDependencies{})
		if code != 2 {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		if strings.Contains(stdout, "login-canary") || strings.Contains(stderr, "login-canary") {
			t.Fatalf("rejected identity leaked login: stdout=%q stderr=%q", stdout, stderr)
		}
	}
}

func TestValidationIdentityCountIsBounded(t *testing.T) {
	var flags validationIdentityFlags
	for index := 0; index < maxValidationIdentities; index++ {
		if err := flags.Set(strings.Repeat("x", 1) + string(rune('a'+index%26)) + fmtInt(index) + "=user" + fmtInt(index) + "@example.com"); err != nil {
			t.Fatalf("Set(%d) error = %v", index, err)
		}
	}
	if err := flags.Set("overflow=overflow@example.com"); err == nil {
		t.Fatal("identity flags accepted more than the maximum")
	}
}

func fmtInt(value int) string {
	const digits = "0123456789"
	if value < 10 {
		return string(digits[value])
	}
	return string(digits[value/10]) + string(digits[value%10])
}

func TestPrivateValidationSourceTokenNeverReturnsLogin(t *testing.T) {
	for _, source := range []string{"alice@example.com", "alice@", "alice"} {
		if got := privateValidationSourceToken(source, source); got != "identity" {
			t.Fatalf("privateValidationSourceToken(%q) = %q", source, got)
		}
	}
	if got := privateValidationSourceToken("group:admin", "alice@example.com"); got != "group:admin" {
		t.Fatalf("group source = %q", got)
	}
}

func TestRenderValidationReportPropagatesWriterErrors(t *testing.T) {
	report := buildValidationReport(validationTestSnapshot(), []validationIdentityInput{{Label: "alpha", Login: "alice@example.com"}, {Label: "beta", Login: "bob@example.com"}}, validationPrivacySummary, "test", time.Now())
	for _, format := range []validationFormat{validationFormatText, validationFormatJSON} {
		if err := renderValidationReport(failingValidationWriter{}, report, format); err == nil {
			t.Fatalf("renderValidationReport(%q) error = nil", format)
		}
	}
}

type failingValidationWriter struct{}

func (failingValidationWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
