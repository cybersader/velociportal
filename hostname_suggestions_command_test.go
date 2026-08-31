package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type hostnameSuggestionFailWriter struct {
	buffer        bytes.Buffer
	failSubstring string
}

func (writer *hostnameSuggestionFailWriter) Write(data []byte) (int, error) {
	if writer.failSubstring == "" || strings.Contains(string(data), writer.failSubstring) {
		return 0, errors.New("write failed")
	}
	return writer.buffer.Write(data)
}

func hostnameSuggestionCommandSnapshot() *CacheData {
	return &CacheData{
		Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.1:*"}}}},
		ProxyHosts: []ProxyHost{{
			ID: 42, Enabled: true, DomainNames: []string{"*.example.com"},
			ForwardHost: "10.0.0.1", ForwardPort: 443,
		}},
		ServiceMetadata: emptyServiceMetadata(),
	}
}

func hostnameSuggestionCommandTestDependencies() hostnameSuggestionCommandDependencies {
	return hostnameSuggestionCommandDependencies{
		loadData: func(context.Context, *Config) (*CacheData, []string, error) {
			return hostnameSuggestionCommandSnapshot(), []string{"grafana.example.com"}, nil
		},
		openTerminal: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("yes\n")), nil
		},
		writeOutput: writeHostnameSuggestionProposal,
	}
}

func TestRunHostnameSuggestionsCommandRequiresPrivateSchemeFlags(t *testing.T) {
	setValidationProcessConfig(t)
	for _, args := range [][]string{
		nil,
		{"--privacy", "summary", "--browser-scheme", "https"},
		{"--privacy", "private"},
		{"--privacy", "private", "--browser-scheme", "ftp"},
		{"--privacy", "private", "--browser-scheme", "https", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		code := runHostnameSuggestionsCommandWithDependencies(args, strings.NewReader("yes\n"), &stdout, &stderr, hostnameSuggestionCommandTestDependencies())
		if code != 2 {
			t.Errorf("args %v exit code = %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("args %v stdout = %q, want empty", args, stdout.String())
		}
	}
}

func TestRunHostnameSuggestionsCommandWarnsOnImplicitAndInactiveProviders(t *testing.T) {
	t.Run("implicit Headscale", func(t *testing.T) {
		setValidationProcessConfig(t)
		t.Setenv("CONTROL_PLANE", "")
		var stdout, stderr bytes.Buffer
		code := runHostnameSuggestionsCommandWithDependencies(
			[]string{"--privacy", "private", "--browser-scheme", "https"},
			strings.NewReader("no\n"), &stdout, &stderr,
			hostnameSuggestionCommandTestDependencies(),
		)
		if code != 0 || !strings.Contains(stderr.String(), implicitHeadscaleDeprecationWarning) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("inactive credentials", func(t *testing.T) {
		setValidationProcessConfig(t)
		clientID := "inactive-client-id-canary"
		secret := "inactive-client-secret-canary"
		t.Setenv("TAILSCALE_OAUTH_CLIENT_ID", clientID)
		t.Setenv("TAILSCALE_OAUTH_CLIENT_SECRET", secret)
		var stdout, stderr bytes.Buffer
		code := runHostnameSuggestionsCommandWithDependencies(
			[]string{"--privacy", "private", "--browser-scheme", "https"},
			strings.NewReader("no\n"), &stdout, &stderr,
			hostnameSuggestionCommandTestDependencies(),
		)
		if code != 0 || !strings.Contains(stderr.String(), "TAILSCALE_OAUTH_CLIENT_ID") || !strings.Contains(stderr.String(), "TAILSCALE_OAUTH_CLIENT_SECRET") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stderr.String(), clientID) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("inactive credential value leaked: %q", stderr.String())
		}
	})
}

func TestRunHostnameSuggestionsCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies([]string{"--help"}, nil, &stdout, &stderr, hostnameSuggestionCommandDependencies{})
	if code != 0 || !strings.Contains(stdout.String(), "--privacy private") || stderr.Len() != 0 {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunHostnameSuggestionsCommandReviewsThenEmitsJSON(t *testing.T) {
	setValidationProcessConfig(t)
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https"},
		strings.NewReader("invalid\nyes\n"),
		&stdout,
		&stderr,
		hostnameSuggestionCommandTestDependencies(),
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "{\n") || strings.Contains(stdout.String(), "WARNING") {
		t.Fatalf("stdout is not proposal-only JSON: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"proxy_host_id": 42`) || !strings.Contains(stdout.String(), `"url": "https://grafana.example.com"`) {
		t.Fatalf("stdout proposal = %q", stdout.String())
	}
	for _, want := range []string{"WARNING: private review", "grafana.example.com", "Enter literal yes or no"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

func TestRunHostnameSuggestionsCommandRejectsWithoutStdout(t *testing.T) {
	setValidationProcessConfig(t)
	for name, input := range map[string]string{"blank": "\n", "no": "no\n"} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runHostnameSuggestionsCommandWithDependencies(
				[]string{"--privacy", "private", "--browser-scheme", "http"},
				strings.NewReader(input), &stdout, &stderr,
				hostnameSuggestionCommandTestDependencies(),
			)
			if code != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Rejected") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunHostnameSuggestionsCommandEOFFailsWithoutStdout(t *testing.T) {
	setValidationProcessConfig(t)
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https"},
		strings.NewReader(""), &stdout, &stderr,
		hostnameSuggestionCommandTestDependencies(),
	)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "confirmation could not be read") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunHostnameSuggestionsCommandRejectsNonTerminatedConfirmation(t *testing.T) {
	setValidationProcessConfig(t)
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https"},
		strings.NewReader("yes"), &stdout, &stderr,
		hostnameSuggestionCommandTestDependencies(),
	)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "confirmation could not be read") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunHostnameSuggestionsCommandAbortsWhenReviewOrPromptCannotBeWritten(t *testing.T) {
	setValidationProcessConfig(t)
	for name, failSubstring := range map[string]string{
		"review": "WARNING: private review",
		"prompt": "Emit this proposal?",
	} {
		t.Run(name, func(t *testing.T) {
			stderr := &hostnameSuggestionFailWriter{failSubstring: failSubstring}
			var stdout bytes.Buffer
			code := runHostnameSuggestionsCommandWithDependencies(
				[]string{"--privacy", "private", "--browser-scheme", "https"},
				strings.NewReader("yes\n"), &stdout, stderr,
				hostnameSuggestionCommandTestDependencies(),
			)
			if code != 1 || stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.buffer.String())
			}
		})
	}
}

func TestRunHostnameSuggestionsCommandUsesTerminalAfterStdinFeed(t *testing.T) {
	setValidationProcessConfig(t)
	dependencies := hostnameSuggestionCommandTestDependencies()
	dependencies.loadData = func(context.Context, *Config) (*CacheData, []string, error) {
		return hostnameSuggestionCommandSnapshot(), nil, nil
	}
	terminalOpened := false
	dependencies.openTerminal = func() (io.ReadCloser, error) {
		terminalOpened = true
		return io.NopCloser(strings.NewReader("yes\n")), nil
	}
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https", "--stdin-hostnames"},
		strings.NewReader("grafana.example.com\n"), &stdout, &stderr, dependencies,
	)
	if code != 0 || !terminalOpened || !strings.Contains(stdout.String(), "grafana.example.com") {
		t.Fatalf("exit=%d terminal=%v stdout=%q stderr=%q", code, terminalOpened, stdout.String(), stderr.String())
	}
}

func TestRunHostnameSuggestionsCommandTerminalFailureHasNoStdout(t *testing.T) {
	setValidationProcessConfig(t)
	dependencies := hostnameSuggestionCommandTestDependencies()
	dependencies.loadData = func(context.Context, *Config) (*CacheData, []string, error) {
		return hostnameSuggestionCommandSnapshot(), nil, nil
	}
	dependencies.openTerminal = func() (io.ReadCloser, error) {
		return nil, errors.New("private terminal canary")
	}
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https", "--stdin-hostnames"},
		strings.NewReader("grafana.example.com\n"), &stdout, &stderr, dependencies,
	)
	if code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "private terminal canary") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestReadHostnameSuggestionStdinBoundsAndValueFreeErrors(t *testing.T) {
	got, err := readHostnameSuggestionStdin(strings.NewReader("\nA.Example.com.\r\n\nB.Example.com\n"))
	if err != nil || len(got) != 2 {
		t.Fatalf("readHostnameSuggestionStdin() = %#v, %v", got, err)
	}

	for name, input := range map[string]string{
		"bytes":   strings.Repeat("\n", maxHostnameSuggestionStdinBytes+1),
		"record":  strings.Repeat("a", maxHostnameSuggestionRecordBytes+1) + ".example.com\n",
		"invalid": "secret_invalid_value\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readHostnameSuggestionStdin(strings.NewReader(input))
			if err == nil {
				t.Fatal("error = nil")
			}
			if strings.Contains(err.Error(), "secret_invalid_value") {
				t.Fatalf("error leaked value: %v", err)
			}
		})
	}

	records := strings.Repeat("a.example.com\n", maxHostnameSuggestionStdinRecords+1)
	if _, err := readHostnameSuggestionStdin(strings.NewReader(records)); err == nil {
		t.Fatal("record limit was accepted")
	}
}

func TestWriteHostnameSuggestionProposalCreatesOwnerOnlyNoClobberFile(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "proposal.json")
	data := []byte("proposal\n")
	if err := writeHostnameSuggestionProposal(output, "", data); err != nil {
		t.Fatalf("writeHostnameSuggestionProposal() error = %v", err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
	if got, err := os.ReadFile(output); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("output = %q, %v", got, err)
	}
	if err := writeHostnameSuggestionProposal(output, "", []byte("replacement")); err == nil {
		t.Fatal("existing output was overwritten")
	}
	if got, _ := os.ReadFile(output); !bytes.Equal(got, data) {
		t.Fatalf("existing output changed to %q", got)
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".velociportal-hostnames-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}

func TestWriteHostnameSuggestionProposalRejectsActiveAndSymlinkPaths(t *testing.T) {
	directory := t.TempDir()
	active := filepath.Join(directory, "active.json")
	if err := os.WriteFile(active, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeHostnameSuggestionProposal(active, active, []byte("proposal")); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("active path error = %v", err)
	}
	if got, _ := os.ReadFile(active); string(got) != "active" {
		t.Fatalf("active file changed to %q", got)
	}

	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeHostnameSuggestionProposal(link, "", []byte("proposal")); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("symlink path error = %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "target" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

func TestRunHostnameSuggestionsCommandOutputModeLeavesStdoutEmpty(t *testing.T) {
	setValidationProcessConfig(t)
	output := filepath.Join(t.TempDir(), "proposal.json")
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https", "--output", output},
		strings.NewReader("yes\n"), &stdout, &stderr,
		hostnameSuggestionCommandTestDependencies(),
	)
	if code != 0 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(data), `"proxy_host_id": 42`) {
		t.Fatalf("output=%q error=%v", data, err)
	}
}

func TestRunHostnameSuggestionsCommandNoCandidatesNeedsNoConfirmation(t *testing.T) {
	setValidationProcessConfig(t)
	dependencies := hostnameSuggestionCommandTestDependencies()
	dependencies.loadData = func(context.Context, *Config) (*CacheData, []string, error) {
		return hostnameSuggestionCommandSnapshot(), []string{"unmatched.other.example"}, nil
	}
	dependencies.openTerminal = func() (io.ReadCloser, error) {
		t.Fatal("terminal opened with no suggestions")
		return nil, nil
	}
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https", "--stdin-hostnames"},
		strings.NewReader("\n"), &stdout, &stderr, dependencies,
	)
	if code != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "No unambiguous") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunHostnameSuggestionsCommandRedactsOperationalErrors(t *testing.T) {
	setValidationProcessConfig(t)
	secret := validConfigValues()["NPM_PASSWORD"]
	dependencies := hostnameSuggestionCommandTestDependencies()
	dependencies.loadData = func(context.Context, *Config) (*CacheData, []string, error) {
		return nil, nil, errors.New("upstream exposed " + secret)
	}
	var stdout, stderr bytes.Buffer
	code := runHostnameSuggestionsCommandWithDependencies(
		[]string{"--privacy", "private", "--browser-scheme", "https"},
		strings.NewReader("yes\n"), &stdout, &stderr, dependencies,
	)
	if code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), secret) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
