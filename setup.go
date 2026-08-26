package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"golang.org/x/term"
)

const headscaleHTTPSetupWarning = "WARNING: Headscale HTTP is allowed only for the canonical internal or same-host route; setup cannot prove route confinement or external inaccessibility."

const setupUsage = `Usage:
  velociportal setup [--env-file FILE]
  velociportal setup observe-proxy [options]

Options:
  --env-file FILE  Read and atomically update FILE (default ".env")
  -h, --help       Show this help

The setup wizard stores upstream credentials locally with mode 0600. It does not
choose TRUSTED_PROXY_CIDR; use "setup observe-proxy" to observe and explicitly
confirm the exact proxy source.
`

type setupSecretReader func(prompt string) ([]byte, error)

type setupCommandDependencies struct {
	readSecret    setupSecretReader
	proxyObserver proxyObserverDependencies
}

func runSetupCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runSetupCommandWithDependencies(args, stdin, stdout, stderr, setupCommandDependencies{})
}

func runSetupCommandWithDependencies(args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies setupCommandDependencies) int {
	stdin, stdout, stderr = normalizeSetupIO(stdin, stdout, stderr)

	if len(args) > 0 && args[0] == "observe-proxy" {
		return runObserveProxyCommandWithDependencies(args[1:], stdin, stdout, stderr, dependencies.proxyObserver)
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, setupUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: setup: %s cannot be combined with other arguments\n\n%s", arg, setupUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := ".env"
	flags.Func("env-file", "environment file to update", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
		return nil
	})
	flags.Usage = func() {}

	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: setup: %v\n\n%s", err, setupUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: setup does not accept positional arguments\n\n%s", setupUsage)
		return 2
	}

	if dependencies.readSecret == nil {
		dependencies.readSecret = terminalSetupSecretReader(stdin, stdout)
	}
	if err := runSetupWizard(envFile, stdin, stdout, stderr, dependencies.readSecret); err != nil {
		fmt.Fprintf(stderr, "velociportal: setup: %v\n", err)
		return 1
	}
	return 0
}

func normalizeSetupIO(stdin io.Reader, stdout, stderr io.Writer) (io.Reader, io.Writer, io.Writer) {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return stdin, stdout, stderr
}

func terminalSetupSecretReader(stdin io.Reader, stdout io.Writer) setupSecretReader {
	return func(string) ([]byte, error) {
		file, ok := stdin.(*os.File)
		if !ok || !term.IsTerminal(int(file.Fd())) {
			return nil, errors.New("hidden secret input requires an interactive terminal")
		}
		secret, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(stdout)
		if err != nil {
			return nil, fmt.Errorf("read hidden input: %w", err)
		}
		return secret, nil
	}
}

func runSetupWizard(envFile string, stdin io.Reader, stdout, stderr io.Writer, readSecret setupSecretReader) error {
	lock, err := acquireEnvFileLock(envFile)
	if err != nil {
		return err
	}
	defer lock.Close()

	original, err := captureEnvFileSnapshot(envFile)
	if err != nil {
		return err
	}
	values, exists, err := readSetupValues(envFile)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Velociportal is a local visibility layer, not an authentication or enforcement service.")
	fmt.Fprintln(stdout, "Keep the raw application port private; only a trusted identity proxy may assert Tailscale-User-* headers.")
	fmt.Fprintln(stdout, "Credentials are written only to the selected local environment file with mode 0600.")
	fmt.Fprintln(stdout, "TRUSTED_PROXY_CIDR will not be guessed. Observe the final proxy path after this wizard.")
	if exists {
		fmt.Fprintf(stdout, "Existing values from %s are offered as defaults. Hidden secrets are never displayed.\n\n", envFile)
	} else {
		fmt.Fprintf(stdout, "Creating %s. Hidden secrets are never displayed.\n\n", envFile)
	}

	reader := bufio.NewReader(stdin)
	values["HEADSCALE_URL"], err = promptSetupValue(reader, stdout, stderr, "Headscale base URL", values["HEADSCALE_URL"], "", normalizeHeadscaleBaseURL)
	if err != nil {
		return err
	}
	if classifyHeadscaleTransport(values["HEADSCALE_URL"]) == headscaleTransportRestrictedHTTP {
		fmt.Fprintln(stderr, headscaleHTTPSetupWarning)
	}
	values["HEADSCALE_API_KEY"], err = promptSetupSecret("Headscale API key", values["HEADSCALE_API_KEY"], readSecret, stdout, stderr)
	if err != nil {
		return err
	}
	values["NPM_URL"], err = promptSetupValue(reader, stdout, stderr, "Nginx Proxy Manager base URL", values["NPM_URL"], "", normalizeNPMBaseURL)
	if err != nil {
		return err
	}
	values["NPM_EMAIL"], err = promptSetupValue(reader, stdout, stderr, "Nginx Proxy Manager email", values["NPM_EMAIL"], "admin@example.com", validateRequiredTrimmedSetupValue("NPM_EMAIL"))
	if err != nil {
		return err
	}
	values["NPM_PASSWORD"], err = promptSetupSecret("Nginx Proxy Manager password", values["NPM_PASSWORD"], readSecret, stdout, stderr)
	if err != nil {
		return err
	}
	values["LISTEN_ADDR"], err = promptSetupValue(reader, stdout, stderr, "Velociportal listen address", values["LISTEN_ADDR"], "127.0.0.1:8080", normalizeListenAddr)
	if err != nil {
		return err
	}
	values["POLL_INTERVAL"], err = promptSetupValue(reader, stdout, stderr, "Upstream poll interval", values["POLL_INTERVAL"], "30s", validateSetupDuration)
	if err != nil {
		return err
	}

	if err := validateExistingTrustedProxy(values["TRUSTED_PROXY_CIDR"]); err != nil {
		return err
	}
	if strings.TrimSpace(values["TRUSTED_PROXY_CIDR"]) == "" {
		values["TRUSTED_PROXY_CIDR"] = ""
	}
	if err := validateSetupConfiguration(values); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}
	if err := writeEnvFileWithSnapshot(envFile, values, &original); err != nil {
		return err
	}

	quotedPath := quoteSetupCommandArgument(envFile)
	fmt.Fprintf(stdout, "\nWrote %s atomically with mode 0600.\n", envFile)
	fmt.Fprintln(stdout, "Keep the temporary observer listener private and reachable only by the final proxy path.")
	fmt.Fprintln(stdout, "Next commands from the repository-guided path:")
	fmt.Fprintln(stdout, "  make observe-proxy")
	fmt.Fprintln(stdout, "  make doctor")
	fmt.Fprintln(stdout, "  make up")
	fmt.Fprintln(stdout, "Direct-binary equivalents:")
	fmt.Fprintf(stdout, "  velociportal setup observe-proxy --env-file %s --listen 127.0.0.1:8080 --timeout 2m\n", quotedPath)
	fmt.Fprintf(stdout, "  velociportal doctor --env-file %s\n", quotedPath)
	fmt.Fprintf(stdout, "  velociportal serve --env-file %s\n", quotedPath)
	return nil
}

func readSetupValues(path string) (map[string]string, bool, error) {
	values, err := readEnvFile(path)
	if err == nil {
		return values, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]string), false, nil
	}
	return nil, false, err
}

func promptSetupValue(reader *bufio.Reader, stdout, stderr io.Writer, label, existing, fallback string, validate func(string) (string, error)) (string, error) {
	if validate == nil {
		return "", errors.New("internal error: setup value validator is unavailable")
	}

	existing = strings.TrimSpace(existing)
	if existing != "" {
		normalized, err := validate(existing)
		if err != nil {
			fmt.Fprintf(stderr, "%s existing value is invalid: %v\n", label, err)
			existing = ""
		} else {
			existing = normalized
		}
	}
	if existing == "" && fallback != "" {
		normalized, err := validate(fallback)
		if err != nil {
			return "", fmt.Errorf("internal error: invalid default for %s: %w", label, err)
		}
		fallback = normalized
	}

	for {
		defaultValue := existing
		if defaultValue == "" {
			defaultValue = fallback
		}
		fmt.Fprint(stdout, label)
		if defaultValue != "" {
			fmt.Fprintf(stdout, " [%s]", defaultValue)
		}
		fmt.Fprint(stdout, ": ")

		line, err := readSetupLine(reader)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", label, err)
		}
		candidate := strings.TrimSpace(line)
		if candidate == "" {
			candidate = defaultValue
		}
		normalized, err := validate(candidate)
		if err != nil {
			fmt.Fprintf(stderr, "%s is invalid: %v\n", label, err)
			continue
		}
		return normalized, nil
	}
}

func promptSetupSecret(label, existing string, readSecret setupSecretReader, stdout, stderr io.Writer) (string, error) {
	if readSecret == nil {
		return "", errors.New("internal error: hidden secret reader is unavailable")
	}
	if existing != "" {
		if err := validateEnvValue(existing); err != nil {
			return "", fmt.Errorf("existing %s is invalid: %w", label, err)
		}
	}

	for {
		prompt := label + " [input hidden]"
		if existing != "" {
			prompt = label + " [set; Enter keeps existing]"
		}
		fmt.Fprint(stdout, prompt+": ")
		secretBytes, err := readSecret(prompt + ": ")
		if err != nil {
			return "", fmt.Errorf("read %s: %w", label, err)
		}
		secret := string(secretBytes)
		for index := range secretBytes {
			secretBytes[index] = 0
		}
		if secret == "" && existing != "" {
			return existing, nil
		}
		if strings.TrimSpace(secret) == "" {
			fmt.Fprintf(stderr, "%s must not be empty.\n", label)
			continue
		}
		if err := validateEnvValue(secret); err != nil {
			fmt.Fprintf(stderr, "%s is invalid: %v\n", label, err)
			continue
		}
		return secret, nil
	}
}

func readSetupLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func validateRequiredTrimmedSetupValue(name string) func(string) (string, error) {
	return func(value string) (string, error) {
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("%s must not be empty", name)
		}
		if err := validateEnvValue(value); err != nil {
			return "", err
		}
		return value, nil
	}
}

func validateSetupDuration(value string) (string, error) {
	duration, err := normalizePollInterval(value)
	if err != nil {
		return "", err
	}
	return duration.String(), nil
}

func quoteSetupCommandArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func validateExistingTrustedProxy(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, _, err := net.ParseCIDR(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("existing TRUSTED_PROXY_CIDR is invalid; run observe-proxy after removing or correcting it: %w", err)
	}
	return nil
}

func validateSetupConfiguration(values map[string]string) error {
	_, err := loadConfigFrom(mapConfigLookup(values))
	if err == nil {
		return nil
	}
	if strings.TrimSpace(values["TRUSTED_PROXY_CIDR"]) == "" && err.Error() == "loadConfig: missing required env: TRUSTED_PROXY_CIDR" {
		return nil
	}
	return err
}
