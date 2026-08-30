package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const hostnameSuggestionsUsage = `Usage:
  velociportal suggest-hostnames --privacy private --browser-scheme http|https [options]

Options:
  --privacy private       Required acknowledgement that review contains private topology
  --browser-scheme VALUE  Browser-facing http or https scheme; never inferred from NPM backends
  --env-file FILE         Load configuration only from FILE instead of process environment
  --stdin-hostnames       Read newline-delimited hostname candidates from stdin
  --output FILE           Create a new owner-only proposal file instead of writing JSON to stdout
  -h, --help              Show this help

The command performs a one-shot private review and emits a service-metadata v1
proposal only after literal confirmation. It never updates active metadata.
`

const (
	maxHostnameSuggestionStdinBytes   = 64 * 1024
	maxHostnameSuggestionStdinRecords = 256
	maxHostnameSuggestionRecordBytes  = 253
)

type hostnameSuggestionCommandDependencies struct {
	loadData     func(context.Context, *Config) (*CacheData, []string, error)
	openTerminal func() (io.ReadCloser, error)
	writeOutput  func(string, string, []byte) error
}

func defaultHostnameSuggestionCommandDependencies() hostnameSuggestionCommandDependencies {
	return hostnameSuggestionCommandDependencies{
		loadData: loadHostnameSuggestionData,
		openTerminal: func() (io.ReadCloser, error) {
			return os.Open("/dev/tty")
		},
		writeOutput: writeHostnameSuggestionProposal,
	}
}

func loadHostnameSuggestionData(ctx context.Context, cfg *Config) (*CacheData, []string, error) {
	if cfg == nil {
		return nil, nil, errors.New("configuration is missing")
	}
	metadata, err := loadServiceMetadataSnapshot(serviceMetadataLoaderForPath(cfg.ServiceMetadataFile))
	if err != nil {
		return nil, nil, err
	}
	controlPlane, npm := newUpstreamClients(cfg)
	suggestionControlPlane, ok := controlPlane.(hostnameSuggestionControlPlane)
	if !ok {
		return nil, nil, errors.New("selected control plane does not support hostname suggestions")
	}
	controlResult, candidateNames, err := suggestionControlPlane.LoadHostnameSuggestions(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	proxyHosts, err := call(ctx, npm.FetchProxyHosts)
	if err != nil {
		return nil, nil, err
	}
	return &CacheData{
		Policy:                    controlResult.Policy,
		Nodes:                     controlResult.Nodes,
		ProxyHosts:                proxyHosts,
		ServiceMetadata:           metadata,
		GrantRoleSelectorsByLogin: controlResult.GrantRoleSelectorsByLogin,
		ControlPlane:              controlResult.Metadata,
	}, candidateNames, nil
}

func runHostnameSuggestionsCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runHostnameSuggestionsCommandWithDependencies(args, stdin, stdout, stderr, defaultHostnameSuggestionCommandDependencies())
}

func runHostnameSuggestionsCommandWithDependencies(args []string, stdin io.Reader, stdout, stderr io.Writer, dependencies hostnameSuggestionCommandDependencies) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, hostnameSuggestionsUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s cannot be combined with other arguments\n\n%s", arg, hostnameSuggestionsUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("suggest-hostnames", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var envFile string
	var privacy string
	var browserScheme string
	var outputPath string
	var stdinHostnames bool
	flags.Func("env-file", "load configuration from file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
		return nil
	})
	flags.StringVar(&privacy, "privacy", "", "privacy mode")
	flags.StringVar(&browserScheme, "browser-scheme", "", "browser-facing scheme")
	flags.BoolVar(&stdinHostnames, "stdin-hostnames", false, "read hostname candidates from stdin")
	flags.Func("output", "create proposal file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		outputPath = value
		return nil
	})
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %v\n\n%s", err, hostnameSuggestionsUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames does not accept positional arguments\n\n%s", hostnameSuggestionsUsage)
		return 2
	}
	if privacy != "private" {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: --privacy private is required\n\n%s", hostnameSuggestionsUsage)
		return 2
	}
	browserScheme = strings.ToLower(strings.TrimSpace(browserScheme))
	if browserScheme != "http" && browserScheme != "https" {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: --browser-scheme must be http or https\n\n%s", hostnameSuggestionsUsage)
		return 2
	}

	lookup, _, secrets, err := validationConfigLookup(envFile)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, nil))
		return 1
	}
	cfg, err := loadConfigFrom(lookup)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	secrets = append(secrets, cfg.ControlPlaneRedactionValues...)
	secrets = append(secrets, cfg.HeadscaleAPIKey, cfg.TailscaleOAuthClientID, cfg.TailscaleOAuthClientSecret, cfg.NPMPassword)
	if !cfg.ControlPlaneExplicit {
		if _, err := fmt.Fprintln(stderr, implicitHeadscaleDeprecationWarning); err != nil {
			return 1
		}
	}
	if len(cfg.InactiveControlPlaneKeys) > 0 {
		if _, err := fmt.Fprintf(stderr, "WARNING: inactive control-plane configuration is ignored: %s\n", strings.Join(cfg.InactiveControlPlaneKeys, ", ")); err != nil {
			return 1
		}
	}

	stdinNames := []string(nil)
	if stdinHostnames {
		stdinNames, err = readHostnameSuggestionStdin(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
			return 1
		}
	}
	if dependencies.loadData == nil {
		dependencies.loadData = loadHostnameSuggestionData
	}
	snapshot, controlPlaneNames, err := dependencies.loadData(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	candidates, err := mergeHostnameSuggestionCandidates(controlPlaneNames, stdinNames)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	eligible, err := eligibleHostnameSuggestionHosts(snapshot, snapshot.ServiceMetadata)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	suggestions, ambiguities, err := buildHostnameSuggestions(candidates, eligible)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	proposal, err := buildHostnameSuggestionProposal(suggestions, eligible, browserScheme)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}

	if err := renderHostnameSuggestionReview(stderr, suggestions, ambiguities); err != nil {
		return 1
	}
	if len(suggestions) == 0 {
		if _, err := fmt.Fprintln(stderr, "No unambiguous hostname suggestions were found. No proposal was written."); err != nil {
			return 1
		}
		return 0
	}

	confirmationReader := stdin
	var terminal io.ReadCloser
	if stdinHostnames {
		if dependencies.openTerminal == nil {
			dependencies.openTerminal = defaultHostnameSuggestionCommandDependencies().openTerminal
		}
		terminal, err = dependencies.openTerminal()
		if err != nil {
			fmt.Fprintln(stderr, "velociportal: suggest-hostnames: controlling terminal is unavailable for confirmation")
			return 1
		}
		defer terminal.Close()
		confirmationReader = terminal
	}
	confirmed, err := confirmHostnameSuggestionProposal(bufio.NewReader(confirmationReader), stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "velociportal: suggest-hostnames: confirmation could not be read")
		return 1
	}
	if !confirmed {
		if _, err := fmt.Fprintln(stderr, "Rejected. No proposal was written."); err != nil {
			return 1
		}
		return 0
	}

	if outputPath == "" {
		if _, err := stdout.Write(proposal); err != nil {
			fmt.Fprintln(stderr, "velociportal: suggest-hostnames: proposal could not be written")
			return 1
		}
		return 0
	}
	if dependencies.writeOutput == nil {
		dependencies.writeOutput = writeHostnameSuggestionProposal
	}
	if err := dependencies.writeOutput(outputPath, cfg.ServiceMetadataFile, proposal); err != nil {
		fmt.Fprintf(stderr, "velociportal: suggest-hostnames: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	fmt.Fprintln(stderr, "Created owner-only hostname proposal file. Review and merge it manually; active metadata was not changed.")
	return 0
}

func readHostnameSuggestionStdin(reader io.Reader) ([]string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxHostnameSuggestionStdinBytes+1))
	if err != nil {
		return nil, errors.New("stdin hostname feed could not be read")
	}
	if len(data) > maxHostnameSuggestionStdinBytes {
		return nil, fmt.Errorf("stdin hostname feed exceeds %d bytes", maxHostnameSuggestionStdinBytes)
	}

	lines := strings.Split(string(data), "\n")
	records := make([]string, 0)
	for lineIndex, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > maxHostnameSuggestionRecordBytes {
			return nil, fmt.Errorf("stdin hostname line %d exceeds %d bytes", lineIndex+1, maxHostnameSuggestionRecordBytes)
		}
		if len(records) >= maxHostnameSuggestionStdinRecords {
			return nil, fmt.Errorf("stdin hostname feed contains more than %d nonblank records", maxHostnameSuggestionStdinRecords)
		}
		if _, valid := normalizeHostnameSuggestion(line); !valid {
			return nil, fmt.Errorf("stdin hostname line %d is invalid", lineIndex+1)
		}
		records = append(records, line)
	}
	return records, nil
}

func renderHostnameSuggestionReview(writer io.Writer, suggestions []hostnameSuggestion, ambiguities []hostnameSuggestionAmbiguity) error {
	lines := []string{
		"WARNING: private review follows. It contains internal hostnames and NPM proxy-host IDs; do not share it publicly.",
		"Provider-supplied names are untrusted suggestions, not proof that they route to the listed NPM service. Verify every destination before merging.",
		"Unambiguous suggestions:",
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return err
		}
	}
	if len(suggestions) == 0 {
		if _, err := fmt.Fprintln(writer, "  none"); err != nil {
			return err
		}
	}
	for _, suggestion := range suggestions {
		if _, err := fmt.Fprintf(writer, "  proxy_host_id=%d hostname=%s source=%s\n", suggestion.ProxyHostID, suggestion.Hostname, suggestion.Source); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(writer, "Excluded ambiguous components:"); err != nil {
		return err
	}
	if len(ambiguities) == 0 {
		if _, err := fmt.Fprintln(writer, "  none"); err != nil {
			return err
		}
	}
	for _, ambiguity := range ambiguities {
		hostnames := make([]string, 0, len(ambiguity.Candidates))
		for _, candidate := range ambiguity.Candidates {
			hostnames = append(hostnames, candidate.Hostname)
		}
		ids := make([]string, 0, len(ambiguity.ProxyHostIDs))
		for _, id := range ambiguity.ProxyHostIDs {
			ids = append(ids, fmt.Sprintf("%d", id))
		}
		if _, err := fmt.Fprintf(writer, "  proxy_host_ids=%s hostnames=%s\n", strings.Join(ids, ","), strings.Join(hostnames, ",")); err != nil {
			return err
		}
	}
	return nil
}

func confirmHostnameSuggestionProposal(reader *bufio.Reader, stderr io.Writer) (bool, error) {
	for {
		if _, err := fmt.Fprint(stderr, "Emit this proposal? Type yes to continue [no]: "); err != nil {
			return false, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, io.EOF
			}
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "yes":
			return true, nil
		case "", "no":
			return false, nil
		default:
			if _, err := fmt.Fprintln(stderr, "Enter literal yes or no."); err != nil {
				return false, err
			}
		}
	}
}

func writeHostnameSuggestionProposal(outputPath, activeMetadataPath string, data []byte) error {
	canonicalOutput, err := canonicalHostnameSuggestionOutputPath(outputPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(activeMetadataPath) != "" {
		canonicalActive, activeErr := canonicalHostnameSuggestionOutputPath(activeMetadataPath)
		if activeErr == nil && canonicalOutput == canonicalActive {
			return errors.New("output path is the configured active service metadata file")
		}
	}
	if _, err := os.Lstat(canonicalOutput); err == nil {
		return errors.New("output path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("output path could not be inspected")
	}

	directory := filepath.Dir(canonicalOutput)
	temporary, err := os.CreateTemp(directory, ".velociportal-hostnames-*")
	if err != nil {
		return errors.New("proposal temporary file could not be created")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	failed := true
	defer func() {
		if failed {
			_ = temporary.Close()
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("proposal temporary file permissions could not be set")
	}
	if _, err := temporary.Write(data); err != nil {
		return errors.New("proposal temporary file could not be written")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("proposal temporary file could not be synchronized")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("proposal temporary file could not be closed")
	}
	if err := os.Link(temporaryPath, canonicalOutput); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("output path already exists")
		}
		return errors.New("proposal file could not be published")
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(canonicalOutput)
		return errors.New("proposal temporary file could not be removed")
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(canonicalOutput)
		return errors.New("proposal directory could not be opened")
	}
	if err := directoryHandle.Sync(); err != nil {
		_ = directoryHandle.Close()
		_ = os.Remove(canonicalOutput)
		return errors.New("proposal directory could not be synchronized")
	}
	if err := directoryHandle.Close(); err != nil {
		_ = os.Remove(canonicalOutput)
		return errors.New("proposal directory could not be closed")
	}
	failed = false
	return nil
}

func canonicalHostnameSuggestionOutputPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("output path must not be empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("output path could not be resolved")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", errors.New("output directory could not be resolved")
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}
