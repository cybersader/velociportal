package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func successfulCLICommands(serve func(configLookup) error) cliCommands {
	if serve == nil {
		serve = func(configLookup) error { return nil }
	}
	return cliCommands{
		serve: serve,
		setup: func([]string, io.Reader, io.Writer, io.Writer) int {
			return 0
		},
		doctor: func([]string, io.Writer, io.Writer) int {
			return 0
		},
		validate: func([]string, io.Writer, io.Writer) int {
			return 0
		},
		suggestHostnames: func([]string, io.Reader, io.Writer, io.Writer) int {
			return 0
		},
		healthcheck: func([]string, io.Writer, io.Writer) int {
			return 0
		},
	}
}

func TestCLIWithoutArgsStartsServeUsingProcessEnvironment(t *testing.T) {
	t.Setenv("CLI_TEST_MARKER", "from-process")
	called := 0
	commands := successfulCLICommands(func(lookup configLookup) error {
		called++
		if value, ok, err := lookup("CLI_TEST_MARKER"); err != nil || !ok || value != "from-process" {
			t.Fatalf("process lookup returned %q, %v, %v", value, ok, err)
		}
		return nil
	})

	var stdout, stderr bytes.Buffer
	code := runCLIWithCommands(nil, nil, &stdout, &stderr, commands)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if called != 1 {
		t.Fatalf("serve calls = %d, want 1", called)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCLIExplicitServeUsesProcessEnvironment(t *testing.T) {
	t.Setenv("CLI_TEST_MARKER", "explicit-serve")
	called := false
	commands := successfulCLICommands(func(lookup configLookup) error {
		called = true
		value, ok, err := lookup("CLI_TEST_MARKER")
		if err != nil || !ok || value != "explicit-serve" {
			t.Fatalf("process lookup returned %q, %v, %v", value, ok, err)
		}
		return nil
	})

	var stdout, stderr bytes.Buffer
	code := runCLIWithCommands([]string{"serve"}, nil, &stdout, &stderr, commands)
	if code != 0 || !called {
		t.Fatalf("exit code = %d, called = %v, stderr = %q", code, called, stderr.String())
	}
}

func TestCLIHelpForms(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		contains string
	}{
		{name: "help command", args: []string{"help"}, contains: "Usage:"},
		{name: "short flag", args: []string{"-h"}, contains: "Commands:"},
		{name: "long flag", args: []string{"--help"}, contains: "Commands:"},
		{name: "help serve", args: []string{"help", "serve"}, contains: "--env-file FILE"},
		{name: "help setup", args: []string{"help", "setup"}, contains: "observe-proxy"},
		{name: "help doctor", args: []string{"help", "doctor"}, contains: "--identity LOGIN"},
		{name: "help validate", args: []string{"help", "validate"}, contains: "LABEL=LOGIN"},
		{name: "help suggest-hostnames", args: []string{"help", "suggest-hostnames"}, contains: "--privacy private"},
		{name: "help healthcheck", args: []string{"help", "healthcheck"}, contains: "--timeout DURATION"},
		{name: "serve short help", args: []string{"serve", "-h"}, contains: "velociportal serve"},
		{name: "serve long help", args: []string{"serve", "--help"}, contains: "velociportal serve"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLIWithCommands(test.args, nil, &stdout, &stderr, successfulCLICommands(nil))
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.contains) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), test.contains)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestCLIUnknownAndUsageErrorsExitTwo(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"wat"}},
		{name: "unknown help topic", args: []string{"help", "wat"}},
		{name: "too many help args", args: []string{"help", "serve", "extra"}},
		{name: "args after help flag", args: []string{"--help", "serve"}},
		{name: "unknown serve flag", args: []string{"serve", "--wat"}},
		{name: "serve missing env file value", args: []string{"serve", "--env-file"}},
		{name: "serve empty env file value", args: []string{"serve", "--env-file="}},
		{name: "serve invalid listen override", args: []string{"serve", "--listen", "0.0.0.0"}},
		{name: "serve help with extra argument", args: []string{"serve", "--help", "extra"}},
		{name: "serve malformed help flag", args: []string{"serve", "-h=true"}},
		{name: "serve positional arg", args: []string{"serve", "extra"}},
		{name: "setup positional arg", args: []string{"setup", "extra"}},
		{name: "doctor positional arg", args: []string{"doctor", "extra"}},
		{name: "validate positional arg", args: []string{"validate", "extra"}},
		{name: "suggest-hostnames positional arg", args: []string{"suggest-hostnames", "extra"}},
		{name: "healthcheck positional arg", args: []string{"healthcheck", "extra"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLIWithCommands(test.args, nil, &stdout, &stderr, defaultCLICommands())
			if code != 2 {
				t.Fatalf("exit code = %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("expected usage error on stderr")
			}
		})
	}
}

func TestCLICommandHandlersReceiveArgsAndStreams(t *testing.T) {
	stdin := strings.NewReader("confirmation\n")
	var stdout, stderr bytes.Buffer
	commands := successfulCLICommands(nil)

	commands.setup = func(args []string, gotStdin io.Reader, gotStdout, gotStderr io.Writer) int {
		if !reflect.DeepEqual(args, []string{"observe-proxy", "--timeout", "5s"}) {
			t.Fatalf("setup args = %v", args)
		}
		if gotStdin != stdin || gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("setup streams were not forwarded")
		}
		return 7
	}
	if code := runCLIWithCommands([]string{"setup", "observe-proxy", "--timeout", "5s"}, stdin, &stdout, &stderr, commands); code != 7 {
		t.Fatalf("setup exit code = %d", code)
	}

	commands.doctor = func(args []string, gotStdout, gotStderr io.Writer) int {
		if !reflect.DeepEqual(args, []string{"--identity", "alice@example.com"}) {
			t.Fatalf("doctor args = %v", args)
		}
		if gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("doctor streams were not forwarded")
		}
		return 8
	}
	if code := runCLIWithCommands([]string{"doctor", "--identity", "alice@example.com"}, stdin, &stdout, &stderr, commands); code != 8 {
		t.Fatalf("doctor exit code = %d", code)
	}

	commands.validate = func(args []string, gotStdout, gotStderr io.Writer) int {
		if !reflect.DeepEqual(args, []string{"--identity", "one=alice@example.com", "--identity", "two=bob@example.com"}) {
			t.Fatalf("validate args = %v", args)
		}
		if gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("validate streams were not forwarded")
		}
		return 9
	}
	if code := runCLIWithCommands([]string{"validate", "--identity", "one=alice@example.com", "--identity", "two=bob@example.com"}, stdin, &stdout, &stderr, commands); code != 9 {
		t.Fatalf("validate exit code = %d", code)
	}

	commands.suggestHostnames = func(args []string, gotStdin io.Reader, gotStdout, gotStderr io.Writer) int {
		if !reflect.DeepEqual(args, []string{"--privacy", "private", "--browser-scheme", "https"}) {
			t.Fatalf("suggest-hostnames args = %v", args)
		}
		if gotStdin != stdin || gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("suggest-hostnames streams were not forwarded")
		}
		return 10
	}
	if code := runCLIWithCommands([]string{"suggest-hostnames", "--privacy", "private", "--browser-scheme", "https"}, stdin, &stdout, &stderr, commands); code != 10 {
		t.Fatalf("suggest-hostnames exit code = %d", code)
	}

	commands.healthcheck = func(args []string, gotStdout, gotStderr io.Writer) int {
		if !reflect.DeepEqual(args, []string{"--timeout", "1s"}) {
			t.Fatalf("healthcheck args = %v", args)
		}
		if gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("healthcheck streams were not forwarded")
		}
		return 9
	}
	if code := runCLIWithCommands([]string{"healthcheck", "--timeout", "1s"}, stdin, &stdout, &stderr, commands); code != 9 {
		t.Fatalf("healthcheck exit code = %d", code)
	}
}

func TestCLIUnavailableHandlersFailCleanly(t *testing.T) {
	for _, commandName := range []string{"serve", "setup", "doctor", "validate", "suggest-hostnames", "healthcheck"} {
		t.Run(commandName, func(t *testing.T) {
			commands := successfulCLICommands(nil)
			switch commandName {
			case "serve":
				commands.serve = nil
			case "setup":
				commands.setup = nil
			case "doctor":
				commands.doctor = nil
			case "validate":
				commands.validate = nil
			case "suggest-hostnames":
				commands.suggestHostnames = nil
			case "healthcheck":
				commands.healthcheck = nil
			}

			var stdout, stderr bytes.Buffer
			code := runCLIWithCommands([]string{commandName}, nil, &stdout, &stderr, commands)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), "handler is unavailable") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestCLIServeEnvFileUsesFileWithoutMutatingEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.env")
	values := validConfigValues()
	values["HEADSCALE_URL"] = "https://file-headscale.example.com/"
	values["NPM_URL"] = "https://file-npm.example.com/"
	values["LISTEN_ADDR"] = "127.0.0.1:8181"
	values["POLL_INTERVAL"] = "20s"
	values["UNKNOWN_KEY"] = "$UNCHANGED"
	if err := writeEnvFile(path, values); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}

	for key := range values {
		t.Setenv(key, "ambient-"+key)
	}
	t.Setenv("AMBIENT_ONLY", "must-not-leak")

	called := false
	commands := successfulCLICommands(func(lookup configLookup) error {
		called = true
		if _, ok, err := lookup("AMBIENT_ONLY"); err != nil || ok {
			t.Fatalf("env-file lookup unexpectedly fell back to process environment: %v", err)
		}
		unknown, ok, err := lookup("UNKNOWN_KEY")
		if err != nil || !ok || unknown != "$UNCHANGED" {
			t.Fatalf("unknown file key = %q, %v, %v", unknown, ok, err)
		}
		cfg, err := loadConfigFrom(lookup)
		if err != nil {
			return err
		}
		if cfg.HeadscaleURL != "https://file-headscale.example.com" {
			t.Fatalf("HeadscaleURL = %q", cfg.HeadscaleURL)
		}
		if cfg.ListenAddr != "127.0.0.1:8181" {
			t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
		}
		return nil
	})

	var stdout, stderr bytes.Buffer
	code := runCLIWithCommands([]string{"serve", "--env-file", path}, nil, &stdout, &stderr, commands)
	if code != 0 || !called {
		t.Fatalf("exit code = %d, called = %v, stderr = %q", code, called, stderr.String())
	}
	for key := range values {
		if got := os.Getenv(key); got != "ambient-"+key {
			t.Fatalf("process environment %s mutated to %q", key, got)
		}
	}
	if got := os.Getenv("AMBIENT_ONLY"); got != "must-not-leak" {
		t.Fatalf("AMBIENT_ONLY mutated to %q", got)
	}
}

func TestCLIServeListenOverrideDoesNotMutateFileValues(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.env")
	values := validConfigValues()
	values["LISTEN_ADDR"] = "127.0.0.1:8080"
	if err := writeEnvFile(path, values); err != nil {
		t.Fatalf("writeEnvFile() error = %v", err)
	}

	commands := successfulCLICommands(func(lookup configLookup) error {
		cfg, err := loadConfigFrom(lookup)
		if err != nil {
			return err
		}
		if cfg.ListenAddr != "0.0.0.0:8080" {
			t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
		}
		return nil
	})
	var stdout, stderr bytes.Buffer
	code := runCLIWithCommands([]string{"serve", "--env-file", path, "--listen", "0.0.0.0:8080"}, nil, &stdout, &stderr, commands)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}

	persisted, err := readEnvFile(path)
	if err != nil {
		t.Fatalf("readEnvFile() error = %v", err)
	}
	if persisted["LISTEN_ADDR"] != "127.0.0.1:8080" {
		t.Fatalf("persisted LISTEN_ADDR = %q", persisted["LISTEN_ADDR"])
	}
}

func TestCLIServeEnvFileErrorsBeforeStartingServer(t *testing.T) {
	directory := t.TempDir()
	malformed := filepath.Join(directory, "malformed.env")
	if err := os.WriteFile(malformed, []byte("KEY=first\nKEY=second\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for name, path := range map[string]string{
		"missing":   filepath.Join(directory, "missing.env"),
		"malformed": malformed,
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			commands := successfulCLICommands(func(configLookup) error {
				called = true
				return nil
			})
			var stdout, stderr bytes.Buffer
			code := runCLIWithCommands([]string{"serve", "--env-file", path}, nil, &stdout, &stderr, commands)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr.String())
			}
			if called {
				t.Fatal("serve handler called after env-file error")
			}
		})
	}
}

func TestCLICommandErrorsExitOne(t *testing.T) {
	commands := successfulCLICommands(func(configLookup) error {
		return errors.New("server failed")
	})
	commands.doctor = func([]string, io.Writer, io.Writer) int {
		return 1
	}

	for _, args := range [][]string{{"serve"}, {"doctor"}} {
		var stdout, stderr bytes.Buffer
		code := runCLIWithCommands(args, nil, &stdout, &stderr, commands)
		if code != 1 {
			t.Fatalf("args %v exit code = %d, want 1", args, code)
		}
	}
}
