package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validDoctorStackEnvValues() map[string]string {
	return map[string]string{
		"VELOCIPORTAL_IMAGE":              "ghcr.io/cybersader/velociportal@sha256:" + strings.Repeat("a", 64),
		"VELOCIPORTAL_SUBNET":             stackEnvDefaultSubnet,
		"VELOCIPORTAL_GATEWAY":            stackEnvDefaultGateway,
		"VELOCIPORTAL_TRUSTED_PROXY_CIDR": stackEnvDefaultGateway + "/32",
	}
}

func writeDoctorStackEnvFile(t *testing.T, values map[string]string) string {
	t.Helper()
	contents, err := serializeEnvFile(values)
	if err != nil {
		t.Fatalf("serialize stack env: %v", err)
	}
	path := filepath.Join(t.TempDir(), "stack.env")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write stack env: %v", err)
	}
	return path
}

func writeDoctorProviderEnvFile(t *testing.T, values map[string]string) string {
	t.Helper()
	contents, err := serializeEnvFile(values)
	if err != nil {
		t.Fatalf("serialize provider env: %v", err)
	}
	path := filepath.Join(t.TempDir(), "velociportal.env")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write provider env: %v", err)
	}
	return path
}

func TestDoctorStackEnvImageChecks(t *testing.T) {
	digest := "ghcr.io/cybersader/velociportal@sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name   string
		image  string
		failed bool
		want   string
	}{
		{name: "missing", failed: true, want: "VELOCIPORTAL_IMAGE is not set"},
		{name: "latest", image: "ghcr.io/cybersader/velociportal:latest", failed: true, want: `must not use the mutable "latest" tag`},
		{name: "untagged", image: "ghcr.io/cybersader/velociportal", failed: true, want: "has no explicit tag or digest"},
		{name: "mutable tag", image: "ghcr.io/cybersader/velociportal:v0.2.0-rc.6", want: `uses mutable tag "v0.2.0-rc.6"`},
		{name: "registry port", image: "registry.example:5000/velociportal:v1", want: `uses mutable tag "v1"`},
		{name: "digest", image: digest, want: "pinned to an immutable sha256 digest"},
		{name: "malformed digest", image: "ghcr.io/cybersader/velociportal@sha256:not-a-digest", failed: true, want: "malformed or unsupported digest pin"},
		{name: "uppercase digest", image: "ghcr.io/cybersader/velociportal@sha256:" + strings.Repeat("A", 64), failed: true, want: "malformed or unsupported digest pin"},
		{name: "double colon", image: "repo::tag", failed: true, want: "valid lowercase Docker image name"},
		{name: "uppercase repository", image: "Foo/bar:v1", failed: true, want: "valid lowercase Docker image name"},
		{name: "surrounding whitespace", image: " ghcr.io/cybersader/velociportal:v1 ", failed: true, want: "must not contain leading or trailing whitespace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			failed := doctorStackEnvImage(&output, map[string]string{"VELOCIPORTAL_IMAGE": test.image})
			if failed != test.failed {
				t.Fatalf("failed = %t, want %t; output=%q", failed, test.failed, output.String())
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output missing %q: %q", test.want, output.String())
			}
		})
	}
}

func TestDoctorStackEnvNetworkChecks(t *testing.T) {
	for _, test := range []struct {
		name   string
		values map[string]string
		failed bool
		want   string
	}{
		{name: "defaults", values: map[string]string{}, want: "172.31.255.1 (default) is contained by VELOCIPORTAL_SUBNET 172.31.255.0/24 (default)"},
		{name: "explicit", values: map[string]string{"VELOCIPORTAL_SUBNET": "10.20.0.0/16", "VELOCIPORTAL_GATEWAY": "10.20.0.1"}, want: "10.20.0.1 (explicit) is contained by VELOCIPORTAL_SUBNET 10.20.0.0/16 (explicit)"},
		{name: "invalid subnet", values: map[string]string{"VELOCIPORTAL_SUBNET": "invalid"}, failed: true, want: "VELOCIPORTAL_SUBNET \"invalid\" is not a valid CIDR"},
		{name: "subnet whitespace", values: map[string]string{"VELOCIPORTAL_SUBNET": " 172.31.255.0/24 "}, failed: true, want: "is not a valid CIDR"},
		{name: "invalid gateway", values: map[string]string{"VELOCIPORTAL_GATEWAY": "invalid"}, failed: true, want: "VELOCIPORTAL_GATEWAY \"invalid\" is not a valid IP address"},
		{name: "gateway whitespace", values: map[string]string{"VELOCIPORTAL_GATEWAY": " 172.31.255.1 "}, failed: true, want: "is not a valid IP address"},
		{name: "outside subnet", values: map[string]string{"VELOCIPORTAL_SUBNET": "10.0.0.0/24"}, failed: true, want: "is not contained by VELOCIPORTAL_SUBNET"},
		{name: "address family mismatch", values: map[string]string{"VELOCIPORTAL_SUBNET": "fd00::/64"}, failed: true, want: "is not contained by VELOCIPORTAL_SUBNET"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			failed := doctorStackEnvNetwork(&output, test.values)
			if failed != test.failed {
				t.Fatalf("failed = %t, want %t; output=%q", failed, test.failed, output.String())
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output missing %q: %q", test.want, output.String())
			}
		})
	}
}

func TestDoctorStackEnvTrustedProxyChecks(t *testing.T) {
	for _, test := range []struct {
		name   string
		values map[string]string
		failed bool
		want   string
	}{
		{name: "missing", values: map[string]string{}, failed: true, want: "VELOCIPORTAL_TRUSTED_PROXY_CIDR is not set"},
		{name: "invalid", values: map[string]string{"VELOCIPORTAL_TRUSTED_PROXY_CIDR": "invalid"}, failed: true, want: "is not a valid CIDR"},
		{name: "whitespace", values: map[string]string{"VELOCIPORTAL_TRUSTED_PROXY_CIDR": " 172.31.255.1/32 "}, failed: true, want: "is not a valid CIDR"},
		{name: "single address and gateway match", values: map[string]string{"VELOCIPORTAL_TRUSTED_PROXY_CIDR": "172.31.255.1/32"}, want: "PASS stack env trusted proxy: address matches VELOCIPORTAL_GATEWAY 172.31.255.1"},
		{name: "multiple addresses", values: map[string]string{"VELOCIPORTAL_TRUSTED_PROXY_CIDR": "172.31.255.1/24"}, want: "contains multiple addresses"},
		{name: "gateway mismatch", values: map[string]string{"VELOCIPORTAL_TRUSTED_PROXY_CIDR": "10.0.0.1/32"}, want: "does not match VELOCIPORTAL_GATEWAY 172.31.255.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			failed := doctorStackEnvTrustedProxy(&output, test.values, nil)
			if failed != test.failed {
				t.Fatalf("failed = %t, want %t; output=%q", failed, test.failed, output.String())
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output missing %q: %q", test.want, output.String())
			}
		})
	}
}

func TestDoctorStackEnvWarnsWhenProviderTrustedProxyIsOverridden(t *testing.T) {
	values := validDoctorStackEnvValues()
	lookup := mapConfigLookup(map[string]string{"TRUSTED_PROXY_CIDR": "127.0.0.1/32"})
	var output bytes.Buffer
	if failed := doctorStackEnvTrustedProxy(&output, values, lookup); failed {
		t.Fatalf("doctorStackEnvTrustedProxy failed: %q", output.String())
	}
	if !strings.Contains(output.String(), "provider env TRUSTED_PROXY_CIDR is overridden in production") {
		t.Fatalf("override warning missing: %q", output.String())
	}

	output.Reset()
	if failed := doctorStackEnvTrustedProxy(&output, values, mapConfigLookup(map[string]string{})); failed {
		t.Fatalf("doctorStackEnvTrustedProxy failed without provider value: %q", output.String())
	}
	if strings.Contains(output.String(), "is overridden in production") {
		t.Fatalf("unexpected override warning: %q", output.String())
	}
}

func TestRunDoctorStackEnvChecksRejectsMissingAndMalformedFiles(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*testing.T) string
	}{
		{name: "missing", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.env") }},
		{name: "malformed", path: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "stack.env")
			if err := os.WriteFile(path, []byte("NO_EQUALS\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			_, failed := runDoctorStackEnvChecks(&output, test.path(t), nil, nil)
			if !failed || !strings.Contains(output.String(), "FAIL stack env:") {
				t.Fatalf("failed=%t output=%q", failed, output.String())
			}
		})
	}
}

func TestRunDoctorStackEnvChecksAppliesComposeInterpolationOverrides(t *testing.T) {
	path := writeDoctorStackEnvFile(t, validDoctorStackEnvValues())
	overrides := mapConfigLookup(map[string]string{
		"VELOCIPORTAL_IMAGE":              "ghcr.io/cybersader/velociportal:latest",
		"VELOCIPORTAL_TRUSTED_PROXY_CIDR": "10.0.0.1/32",
	})
	var output bytes.Buffer
	trustedProxyCIDR, failed := runDoctorStackEnvChecks(&output, path, nil, overrides)
	if !failed {
		t.Fatalf("overridden latest image did not fail: %q", output.String())
	}
	if trustedProxyCIDR != "10.0.0.1/32" {
		t.Fatalf("trusted proxy = %q", trustedProxyCIDR)
	}
	for _, want := range []string{
		"process environment overrides VELOCIPORTAL_IMAGE",
		"process environment overrides VELOCIPORTAL_TRUSTED_PROXY_CIDR",
		`must not use the mutable "latest" tag`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %q", want, output.String())
		}
	}
}

func TestDoctorStackEnvDefaultsMatchProductionCompose(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("deploy", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"${VELOCIPORTAL_SUBNET:-" + stackEnvDefaultSubnet + "}",
		"${VELOCIPORTAL_GATEWAY:-" + stackEnvDefaultGateway + "}",
	} {
		if !bytes.Contains(contents, []byte(want)) {
			t.Errorf("production Compose missing %q", want)
		}
	}
}

func TestRunDoctorStackEnvChecksAcceptsExample(t *testing.T) {
	var output bytes.Buffer
	trustedProxyCIDR, failed := runDoctorStackEnvChecks(&output, filepath.Join("deploy", "stack.env.example"), nil, nil)
	if failed {
		t.Fatalf("example failed: %q", output.String())
	}
	if trustedProxyCIDR != stackEnvDefaultGateway+"/32" {
		t.Fatalf("trusted proxy = %q", trustedProxyCIDR)
	}
	if strings.Contains(output.String(), "FAIL stack env") || !strings.Contains(output.String(), "WARN stack env image:") {
		t.Fatalf("unexpected example report: %q", output.String())
	}
}

func TestRunDoctorCommandStackEnvUsesEffectiveProductionProxy(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	providerValues := doctorFixtureConfig(fixture)
	delete(providerValues, "TRUSTED_PROXY_CIDR")
	providerEnv := writeDoctorProviderEnvFile(t, providerValues)
	stackEnv := writeDoctorStackEnvFile(t, validDoctorStackEnvValues())

	code, stdout, stderr := runDoctorForTest([]string{"--env-file", providerEnv, "--stack-env", stackEnv}, fixture)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, want := range []string{
		"PASS stack env image:",
		"PASS stack env network:",
		"PASS stack env trusted proxy:",
		"PASS trusted proxy CIDR: 172.31.255.1/32 contains one address",
		"PASS doctor: required diagnostics completed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunDoctorCommandStackEnvOnlyIsOffline(t *testing.T) {
	stackEnv := writeDoctorStackEnvFile(t, validDoctorStackEnvValues())
	code, stdout, stderr := runDoctorForTest([]string{"--stack-env", stackEnv})
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "PASS doctor: stack environment diagnostics completed") {
		t.Fatalf("stack-only completion missing: %q", stdout)
	}
	if strings.Contains(stdout, "config source") || strings.Contains(stdout, "NPM authentication") {
		t.Fatalf("stack-only doctor continued into configuration or upstream diagnostics: %q", stdout)
	}

	code, _, stderr = runDoctorForTest([]string{"--stack-env", stackEnv, "--identity", "alice@example.com"})
	if code != 2 || !strings.Contains(stderr, "--identity requires configuration") {
		t.Fatalf("stack-only identity exit=%d stderr=%q", code, stderr)
	}
}

func TestRunDoctorCommandStackEnvOmittedIsBackwardCompatible(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	setDoctorProcessConfig(t, doctorFixtureConfig(fixture))
	code, stdout, stderr := runDoctorForTest(nil, fixture)
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "stack env") {
		t.Fatalf("doctor without --stack-env emitted stack diagnostics: %q", stdout)
	}
}
