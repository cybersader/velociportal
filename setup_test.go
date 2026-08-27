package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type secretQueue struct {
	values []string
	index  int
}

func (queue *secretQueue) read(prompt string) ([]byte, error) {
	if queue.index >= len(queue.values) {
		return nil, io.EOF
	}
	value := []byte(queue.values[queue.index])
	queue.index++
	return value, nil
}

func TestRunSetupCommandCreatesValidatedLocalConfiguration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "velociportal.env")
	stdin := strings.NewReader(strings.Join([]string{
		"",
		"https://headscale.example.com/control/",
		"http://npm.velociportal.internal:81/",
		"portal@example.com",
		"127.0.0.1:9090",
		"45s",
	}, "\n") + "\n")
	secrets := &secretQueue{values: []string{"headscale-secret-key", "npm-secret-password"}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		stdin,
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}

	values, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	want := map[string]string{
		"CONTROL_PLANE":      "headscale",
		"HEADSCALE_URL":      "https://headscale.example.com/control",
		"HEADSCALE_API_KEY":  "headscale-secret-key",
		"NPM_URL":            "http://npm.velociportal.internal:81",
		"NPM_EMAIL":          "portal@example.com",
		"NPM_PASSWORD":       "npm-secret-password",
		"LISTEN_ADDR":        "127.0.0.1:9090",
		"POLL_INTERVAL":      "45s",
		"TRUSTED_PROXY_CIDR": "",
	}
	for key, wantValue := range want {
		if got := values[key]; got != wantValue {
			t.Errorf("%s = %q, want %q", key, got, wantValue)
		}
	}

	validated := cloneStrings(values)
	validated["TRUSTED_PROXY_CIDR"] = "192.0.2.10/32"
	if _, err := loadConfigFrom(mapConfigLookup(validated)); err != nil {
		t.Fatalf("written values do not pass loadConfigFrom after proxy observation: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}

	combined := stdout.String() + stderr.String()
	for _, secret := range []string{"headscale-secret-key", "npm-secret-password"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("output leaked secret %q: %q", secret, combined)
		}
	}
	for _, text := range []string{
		"visibility layer, not an authentication or enforcement service",
		"TRUSTED_PROXY_CIDR will not be guessed",
		"setup observe-proxy",
		"velociportal doctor",
		"velociportal serve",
	} {
		if !strings.Contains(stdout.String(), text) {
			t.Errorf("stdout missing %q: %q", text, stdout.String())
		}
	}
	if strings.Contains(stderr.String(), headscaleHTTPSetupWarning) {
		t.Fatalf("HTTPS setup emitted the Headscale HTTP warning: %q", stderr.String())
	}
}

func TestRunSetupCommandWarnsForAcceptedHeadscaleHTTPWithoutLeakingRouteOrSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	stdin := strings.NewReader(strings.Join([]string{
		"",
		"http://headscale.velociportal.internal:8080/control/",
		"http://npm.velociportal.internal:81/",
		"portal@example.com",
		"127.0.0.1:9090",
		"45s",
	}, "\n") + "\n")
	secrets := &secretQueue{values: []string{"http-headscale-secret", "http-npm-secret"}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		stdin,
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), headscaleHTTPSetupWarning) {
		t.Fatalf("stderr missing Headscale HTTP warning: %q", stderr.String())
	}
	for _, forbidden := range []string{
		"headscale.velociportal.internal",
		":8080",
		"http-headscale-secret",
		"http-npm-secret",
	} {
		if strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("Headscale HTTP warning leaked %q: %q", forbidden, stderr.String())
		}
	}

	values, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	if values["HEADSCALE_URL"] != "http://headscale.velociportal.internal:8080/control" {
		t.Fatalf("HEADSCALE_URL = %q", values["HEADSCALE_URL"])
	}
}

func TestRunSetupCommandPrefillsAndPreservesExistingValuesAndUnknownKeys(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := map[string]string{
		"CONTROL_PLANE":          "headscale",
		"HEADSCALE_URL":          "https://headscale.example.com",
		"HEADSCALE_API_KEY":      "existing-headscale-secret",
		"NPM_URL":                "https://npm.example.com",
		"NPM_EMAIL":              "existing@example.com",
		"NPM_PASSWORD":           "existing-npm-secret",
		"LISTEN_ADDR":            "127.0.0.1:8080",
		"POLL_INTERVAL":          "30s",
		"TRUSTED_PROXY_CIDR":     "198.51.100.7/32",
		"UNKNOWN_FUTURE_SETTING": "preserve-me",
	}
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}
	secrets := &secretQueue{values: []string{"", ""}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		strings.NewReader("\n\n\n\n\n\n"),
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}

	got, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	for key, want := range initial {
		if got[key] != want {
			t.Errorf("%s = %q, want preserved %q", key, got[key], want)
		}
	}
	combined := stdout.String() + stderr.String()
	for _, secret := range []string{initial["HEADSCALE_API_KEY"], initial["NPM_PASSWORD"]} {
		if strings.Contains(combined, secret) {
			t.Fatalf("output leaked existing secret %q", secret)
		}
	}
	if !strings.Contains(stdout.String(), "[set; Enter keeps existing]") {
		t.Fatalf("stdout did not indicate secret preservation without showing it: %q", stdout.String())
	}
}

func TestRunSetupCommandValidatesEachAnswerImmediatelyWithoutLeakingRejectedSecrets(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	stdin := strings.NewReader(strings.Join([]string{
		"",
		"ftp://headscale.example.com",
		"http://headscale.example.com",
		"https://headscale.example.com/",
		"npm-without-a-url",
		"http://npm.example.com:81",
		"https://npm.example.com/",
		"operator@example.com",
		"example.com:8080",
		"0.0.0.0:8080",
		"0s",
		"15s",
	}, "\n") + "\n")
	secrets := &secretQueue{values: []string{"", "accepted-api-key", "rejected\npassword", "accepted-password"}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		stdin,
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, text := range []string{
		"Headscale base URL is invalid",
		"Headscale API key must not be empty",
		"Nginx Proxy Manager base URL is invalid",
		"Velociportal listen address is invalid",
		"Upstream poll interval is invalid",
		"Nginx Proxy Manager password is invalid",
	} {
		if !strings.Contains(stderr.String(), text) {
			t.Errorf("stderr missing immediate validation message %q: %q", text, stderr.String())
		}
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "rejected\npassword") || strings.Contains(combined, "accepted-api-key") || strings.Contains(combined, "accepted-password") {
		t.Fatalf("output leaked a secret: %q", combined)
	}

	values, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	if values["HEADSCALE_URL"] != "https://headscale.example.com" || values["NPM_URL"] != "https://npm.example.com" {
		t.Fatalf("URLs were not normalized: %#v", values)
	}
	if values["LISTEN_ADDR"] != "0.0.0.0:8080" || values["POLL_INTERVAL"] != "15s" {
		t.Fatalf("validated values were not stored: %#v", values)
	}
}

func TestRunSetupCommandRefusesVisibleSecretInputOnNonTTY(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	var stdout, stderr bytes.Buffer
	code := runSetupCommand(
		[]string{"--env-file", path},
		strings.NewReader("\nhttps://headscale.example.com\nvisible-secret-that-must-not-be-read\n"),
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "hidden secret input requires an interactive terminal") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "visible-secret-that-must-not-be-read") {
		t.Fatal("non-TTY input was echoed or treated as a secret")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment file exists after non-TTY refusal: %v", err)
	}
}

func TestRunSetupCommandLeavesExistingFileUntouchedOnAbortedWizard(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := validConfigValues()
	initial["LISTEN_ADDR"] = "127.0.0.1:8080"
	initial["POLL_INTERVAL"] = "30s"
	initial["UNKNOWN_KEY"] = "keep"
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	secrets := &secretQueue{values: []string{""}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		strings.NewReader("\n"),
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("file changed after aborted wizard:\nbefore=%q\nafter=%q", before, after)
	}
	for _, secret := range []string{initial["HEADSCALE_API_KEY"], initial["NPM_PASSWORD"]} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("output leaked secret %q", secret)
		}
	}
}

func TestRunSetupCommandRejectsInvalidExistingTrustedProxyWithoutReplacingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	initial := validConfigValues()
	initial["LISTEN_ADDR"] = "127.0.0.1:8080"
	initial["POLL_INTERVAL"] = "30s"
	initial["TRUSTED_PROXY_CIDR"] = "not-a-cidr"
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	secrets := &secretQueue{values: []string{"", ""}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		strings.NewReader("\n\n\n\n\n\n"),
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "existing TRUSTED_PROXY_CIDR is invalid") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("invalid existing file was replaced: before=%q after=%q", before, after)
	}
}

func TestRunSetupCommandCreatesTailscalePreviewConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	stdin := strings.NewReader(strings.Join([]string{
		"tailscale",
		"oauth-client-id",
		"https://npm.example.com",
		"portal@example.com",
		"127.0.0.1:9090",
		"45s",
	}, "\n") + "\n")
	secrets := &secretQueue{values: []string{"oauth-client-secret", "npm-secret"}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		stdin,
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	values, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	if values["CONTROL_PLANE"] != "tailscale" || values["TAILSCALE_OAUTH_CLIENT_ID"] != "oauth-client-id" || values["TAILSCALE_OAUTH_CLIENT_SECRET"] != "oauth-client-secret" {
		t.Fatalf("Tailscale values = %#v", values)
	}
	for _, key := range headscaleRequiredConfigKeys {
		if _, ok := values[key]; ok {
			t.Fatalf("Tailscale setup retained inactive key %s", key)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), "oauth-client-id") || strings.Contains(stdout.String()+stderr.String(), "oauth-client-secret") {
		t.Fatalf("setup output exposed OAuth credential material: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "tailscale=preview") {
		t.Fatalf("setup output did not label Tailscale preview: %q", stdout.String())
	}
}

func TestRunSetupCommandProviderSwitchConfirmsRemovalAndPreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	initial := validConfigValues()
	initial["LISTEN_ADDR"] = "127.0.0.1:8080"
	initial["POLL_INTERVAL"] = "30s"
	initial["UNKNOWN_KEY"] = "preserve-me"
	if err := writeEnvFile(path, initial); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}
	stdin := strings.NewReader(strings.Join([]string{
		"tailscale",
		"yes",
		"oauth-client-id",
		"",
		"",
		"",
		"",
	}, "\n") + "\n")
	secrets := &secretQueue{values: []string{"oauth-client-secret", ""}}
	var stdout, stderr bytes.Buffer

	code := runSetupCommandWithDependencies(
		[]string{"--env-file", path},
		stdin,
		&stdout,
		&stderr,
		setupCommandDependencies{readSecret: secrets.read},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	values, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	if values["CONTROL_PLANE"] != "tailscale" || values["UNKNOWN_KEY"] != "preserve-me" {
		t.Fatalf("switched values = %#v", values)
	}
	for _, key := range headscaleRequiredConfigKeys {
		if _, ok := values[key]; ok {
			t.Fatalf("confirmed switch retained inactive key %s", key)
		}
	}
	for _, key := range headscaleRequiredConfigKeys {
		if !strings.Contains(stdout.String(), key) {
			t.Errorf("switch confirmation did not list %s: %q", key, stdout.String())
		}
	}
}

func TestRunSetupCommandProviderSwitchRefusalAndAbortAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name  string
		stdin string
	}{
		{name: "refused", stdin: "tailscale\nno\n"},
		{name: "aborted", stdin: "tailscale\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			initial := validConfigValues()
			initial["LISTEN_ADDR"] = "127.0.0.1:8080"
			initial["POLL_INTERVAL"] = "30s"
			initial["UNKNOWN_KEY"] = "preserve-me"
			if err := writeEnvFile(path, initial); err != nil {
				t.Fatalf("writeEnvFile() error = %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			var stdout, stderr bytes.Buffer
			code := runSetupCommandWithDependencies(
				[]string{"--env-file", path},
				strings.NewReader(test.stdin),
				&stdout,
				&stderr,
				setupCommandDependencies{readSecret: (&secretQueue{}).read},
			)
			if code != 1 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(after) error = %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("file changed after %s switch: before=%q after=%q", test.name, before, after)
			}
		})
	}
}

func TestRunSetupCommandUsageAndArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown"},
		{"--env-file"},
		{"--env-file="},
		{"extra"},
		{"--help", "extra"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runSetupCommand(args, strings.NewReader(""), &stdout, &stderr)
			if code != 2 {
				t.Fatalf("args %v exit code = %d, want 2; stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("expected usage error on stderr")
			}
		})
	}

	for _, args := range [][]string{{"--help"}, {"-h"}, {"observe-proxy", "--help"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runSetupCommand(args, strings.NewReader(""), &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("args %v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestPromptSetupSecretPreservesExistingOnBlankAndNeverWritesIt(t *testing.T) {
	secret := "existing-secret"
	queue := &secretQueue{values: []string{""}}
	var stderr bytes.Buffer
	got, err := promptSetupSecret("API key", secret, queue.read, io.Discard, &stderr)
	if err != nil {
		t.Fatalf("promptSetupSecret() error = %v", err)
	}
	if got != secret {
		t.Fatalf("secret = %q, want preserved value", got)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr leaked secret: %q", stderr.String())
	}
}

func TestQuoteSetupCommandArgumentIsSafeForSpacesQuotesAndExpansionCharacters(t *testing.T) {
	got := quoteSetupCommandArgument("config dir/it's-$HOME.env")
	want := `'config dir/it'"'"'s-$HOME.env'`
	if got != want {
		t.Fatalf("quoteSetupCommandArgument() = %q, want %q", got, want)
	}
}
