package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const doctorUsage = `Usage:
  velociportal doctor [--env-file FILE] [--stack-env FILE] [--identity LOGIN ...]

Options:
  --env-file FILE    Load configuration only from FILE instead of process environment
  --stack-env FILE   Check a Compose stack.env file; combine with --env-file for full diagnostics
  --identity LOGIN   Preview cards for LOGIN; may be repeated
  -h, --help         Show this help
`

const (
	maxDoctorIdentityLength    = 320
	maxDoctorErrorLength       = 240
	maxDoctorErrorWork         = 4096
	headscaleHTTPDoctorWarning = "WARN Headscale HTTP route: private Docker/host route confinement and external inaccessibility are not proven"
)

var (
	doctorHTTPBodyRE       = regexp.MustCompile(`(?is)((?:returned|unexpected) status [0-9]{3}):.*$`)
	doctorSensitiveFieldRE = regexp.MustCompile(`(?i)(["']?(?:token|secret|password|api[_-]?key|authorization)["']?\s*[:=]\s*["']?)([^"',}\s]+)`)
	doctorBearerRE         = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	doctorJWTRE            = regexp.MustCompile(`[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
)

type doctorIdentityFlags []string

type doctorDependencies struct {
	newClients             func(*Config) (ControlPlane, *NPMClient)
	newServiceHealthProber func(*Config, *ServiceHealthConfig) serviceHealthProber
}

func defaultDoctorDependencies() doctorDependencies {
	return doctorDependencies{
		newClients: newUpstreamClients,
		newServiceHealthProber: func(cfg *Config, config *ServiceHealthConfig) serviceHealthProber {
			engine, err := newServiceProbeEngine(config, serviceHealthProtectedURLs(cfg))
			if err != nil {
				return nil
			}
			return engine
		},
	}
}

func (values *doctorIdentityFlags) String() string {
	return strings.Join(*values, ",")
}

func (values *doctorIdentityFlags) Set(raw string) error {
	identity := strings.TrimSpace(raw)
	if identity == "" {
		return errors.New("identity must not be empty")
	}
	if strings.ContainsAny(identity, "\r\n\x00") {
		return errors.New("identity must not contain CR, LF, or NUL")
	}
	if len([]rune(identity)) > maxDoctorIdentityLength {
		return fmt.Errorf("identity exceeds %d characters", maxDoctorIdentityLength)
	}
	*values = append(*values, identity)
	return nil
}

func runDoctorCommand(args []string, stdout, stderr io.Writer) int {
	return runDoctorCommandWithDependencies(args, stdout, stderr, defaultDoctorDependencies())
}

func runDoctorCommandWithDependencies(args []string, stdout, stderr io.Writer, dependencies doctorDependencies) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, doctorUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: doctor: %s cannot be combined with other arguments\n\n%s", arg, doctorUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var envFile string
	var stackEnvFile string
	var identities doctorIdentityFlags
	flags.Func("env-file", "load configuration from file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
		return nil
	})
	flags.Func("stack-env", "run offline checks against a Compose stack.env file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		stackEnvFile = value
		return nil
	})
	flags.Var(&identities, "identity", "preview cards for an identity")
	flags.Usage = func() {}

	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: doctor: %v\n\n%s", err, doctorUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: doctor does not accept positional arguments\n\n%s", doctorUsage)
		return 2
	}
	if stackEnvFile != "" && envFile == "" {
		if len(identities) > 0 {
			fmt.Fprintf(stderr, "velociportal: doctor: --identity requires configuration; combine --stack-env with --env-file\n\n%s", doctorUsage)
			return 2
		}
		_, failed := runDoctorStackEnvChecks(stdout, stackEnvFile, nil, processConfigLookup)
		if failed {
			return 1
		}
		fmt.Fprintln(stdout, "PASS doctor: stack environment diagnostics completed")
		return 0
	}

	lookup := configLookup(processConfigLookup)
	if envFile == "" {
		fmt.Fprintln(stdout, "PASS config source: process environment")
		fmt.Fprintln(stdout, "PASS env file mode: not applicable")
	} else {
		values, err := readEnvFile(envFile)
		if err != nil {
			fmt.Fprintf(stdout, "FAIL config source: %s\n", sanitizeDoctorError(err, nil))
			return 1
		}
		lookup = mapConfigLookup(values)
		fmt.Fprintln(stdout, "PASS config source: environment file")

		info, err := os.Stat(envFile)
		if err != nil {
			fmt.Fprintf(stdout, "FAIL env file mode: %s\n", sanitizeDoctorError(err, doctorSecretValues(lookup)))
			return 1
		}
		if !info.Mode().IsRegular() {
			fmt.Fprintln(stdout, "FAIL env file mode: configuration path is not a regular file")
			return 1
		}
		permissions := info.Mode().Perm()
		if permissions&0o077 != 0 {
			fmt.Fprintf(stdout, "FAIL env file mode: permissions %04o allow group or other access; require owner-only permissions\n", permissions)
			return 1
		}
		fmt.Fprintf(stdout, "PASS env file mode: owner-only permissions (%04o)\n", permissions)
	}

	if stackEnvFile != "" {
		trustedProxyCIDR, failed := runDoctorStackEnvChecks(stdout, stackEnvFile, lookup, processConfigLookup)
		if failed {
			return 1
		}
		lookup = doctorStackEnvConfigLookup(lookup, trustedProxyCIDR)
	}

	secrets := doctorSecretValues(lookup)
	cfg, err := loadConfigFrom(lookup)
	if err != nil {
		fmt.Fprintf(stdout, "FAIL config: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	secrets = append(secrets, cfg.ControlPlaneRedactionValues...)
	secrets = append(secrets, cfg.HeadscaleAPIKey, cfg.TailscaleOAuthClientID, cfg.TailscaleOAuthClientSecret, cfg.NPMPassword)
	fmt.Fprintln(stdout, "PASS config: required values validated")
	if cfg.ControlPlaneExplicit {
		fmt.Fprintf(stdout, "PASS control plane selection: %s (explicit)\n", cfg.ControlPlane)
	} else {
		fmt.Fprintln(stdout, "WARN control plane selection: "+implicitHeadscaleDeprecationMessage)
	}
	if len(cfg.InactiveControlPlaneKeys) > 0 {
		fmt.Fprintf(stdout, "WARN inactive control-plane configuration: ignoring %s\n", strings.Join(cfg.InactiveControlPlaneKeys, ", "))
	}

	reportDoctorSingleAddressCIDR(stdout, "trusted proxy CIDR", cfg.TrustedProxyCIDR.String(), cfg.TrustedProxyCIDR)
	if cfg.ControlPlane == controlPlaneHeadscale && classifyHeadscaleTransport(cfg.HeadscaleURL) == headscaleTransportRestrictedHTTP {
		fmt.Fprintln(stdout, headscaleHTTPDoctorWarning)
	}

	metadata, err := loadServiceMetadataSnapshot(serviceMetadataLoaderForPath(cfg.ServiceMetadataFile))
	if err != nil {
		stage, cause := snapshotStageFailure(err)
		fmt.Fprintf(stdout, "FAIL %s: %s\n", stage, sanitizeDoctorError(cause, secrets))
		fmt.Fprintf(stdout, "FAIL snapshot: not created because %s failed\n", stage)
		return 1
	}
	if cfg.ServiceMetadataFile == "" {
		fmt.Fprintln(stdout, "PASS service metadata: disabled")
	} else {
		fmt.Fprintf(stdout, "PASS service metadata: loaded %d override(s)\n", len(metadata.Overrides))
	}

	healthConfig, healthConfigErr := serviceHealthConfigLoaderForPath(cfg.ServiceHealthFile)()
	healthConfigFailed := healthConfigErr != nil || healthConfig == nil
	if healthConfigErr != nil {
		fmt.Fprintf(stdout, "FAIL service health configuration: %s\n", sanitizeDoctorError(healthConfigErr, secrets))
	} else if healthConfig == nil {
		fmt.Fprintln(stdout, "FAIL service health configuration: loader returned an incomplete result")
	} else if !healthConfig.Enabled {
		fmt.Fprintln(stdout, "PASS service health configuration: disabled")
	} else {
		fmt.Fprintf(stdout, "PASS service health configuration: loaded %d target(s)\n", len(healthConfig.Services))
	}

	defaults := defaultDoctorDependencies()
	if dependencies.newClients == nil {
		dependencies.newClients = defaults.newClients
	}
	if dependencies.newServiceHealthProber == nil {
		dependencies.newServiceHealthProber = defaults.newServiceHealthProber
	}
	controlPlane, npm := dependencies.newClients(cfg)

	progress := func(stage snapshotLoadStage, count int) {
		switch stage {
		case snapshotStageHeadscalePolicy:
			fmt.Fprintf(stdout, "PASS Headscale policy: loaded %d access rules\n", count)
		case snapshotStageHeadscaleNodes:
			fmt.Fprintf(stdout, "PASS Headscale nodes: loaded %d nodes\n", count)
		case snapshotStageTailscaleOAuth:
			fmt.Fprintln(stdout, "PASS Tailscale OAuth: access token acquired")
		case snapshotStageTailscalePolicy:
			fmt.Fprintf(stdout, "PASS Tailscale policy: loaded %d access rules\n", count)
		case snapshotStageTailscaleUsers:
			fmt.Fprintf(stdout, "PASS Tailscale users: loaded %d users\n", count)
		case snapshotStageTailscaleDevices:
			fmt.Fprintf(stdout, "PASS Tailscale devices: loaded %d devices\n", count)
		case snapshotStageNPMAuth:
			fmt.Fprintln(stdout, "PASS NPM authentication: credentials accepted")
		case snapshotStageNPMProxyHosts:
			fmt.Fprintf(stdout, "PASS NPM proxy hosts: loaded %d proxy hosts\n", count)
		}
	}

	snapshot, err := loadSnapshotWithProgress(context.Background(), controlPlane, npm, progress)
	if err != nil {
		stage, cause := snapshotStageFailure(err)
		secrets = append(secrets, doctorControlPlaneToken(controlPlane), doctorNPMToken(npm))
		fmt.Fprintf(stdout, "FAIL %s: %s\n", stage, sanitizeDoctorError(cause, secrets))
		fmt.Fprintf(stdout, "FAIL snapshot: not created because %s failed\n", stage)
		return 1
	}
	if snapshot == nil || snapshot.Policy == nil {
		fmt.Fprintln(stdout, "FAIL snapshot: loader returned an incomplete snapshot")
		return 1
	}
	snapshot.ServiceMetadata = metadata
	fmt.Fprintf(stdout, "PASS snapshot: complete (%d access rules, %d nodes, %d proxy hosts)\n", snapshot.Policy.accessRuleCount(), len(snapshot.Nodes), len(snapshot.ProxyHosts))
	if unmatched := unmatchedServiceMetadataCount(metadata, snapshot.ProxyHosts); unmatched > 0 {
		fmt.Fprintf(stdout, "WARN service metadata targets: %d override(s) do not match a current NPM proxy host ID\n", unmatched)
	}
	fmt.Fprintf(
		stdout,
		"PASS control plane metadata: provider=%s policy_mode=%s support_level=%s\n",
		snapshot.ControlPlane.Provider,
		snapshot.ControlPlane.PolicyMode,
		snapshot.ControlPlane.SupportLevel,
	)
	switch snapshot.Policy.SSH.State {
	case sshPolicySupported:
		fmt.Fprintf(stdout, "PASS policy support: SSH Machines view requires matching SSH policy, Grant TCP/22, and reported SSH-capable device evidence (state=%s rules=%d)\n", snapshot.Policy.SSH.State, snapshot.Policy.SSH.RuleCount)
	case sshPolicyUnsupported:
		fmt.Fprintf(stdout, "WARN policy support: SSH Machines view is suppressed while HTTP service cards remain valid (state=%s reason=%s rules=%d)\n", snapshot.Policy.SSH.State, snapshot.Policy.SSH.UnsupportedReason, snapshot.Policy.SSH.RuleCount)
	}

	reportDoctorJoinCoverage(stdout, snapshot)
	reportDoctorIdentityPreviews(stdout, identities, snapshot)
	if healthConfig != nil && healthConfig.Enabled {
		reportDoctorServiceHealth(stdout, cfg, healthConfig, snapshot, dependencies.newServiceHealthProber)
	}
	fmt.Fprintln(stdout, "WARN validation scope: join and card results are supported matcher previews, not proof of network authorization or reachability")
	if healthConfigFailed {
		fmt.Fprintln(stdout, "FAIL doctor: service health configuration is invalid")
		return 1
	}
	fmt.Fprintln(stdout, "PASS doctor: required diagnostics completed")
	return 0
}

func reportDoctorServiceHealth(
	stdout io.Writer,
	cfg *Config,
	config *ServiceHealthConfig,
	snapshot *CacheData,
	newProber func(*Config, *ServiceHealthConfig) serviceHealthProber,
) {
	cache := &Cache{}
	cache.data.Store(snapshot)
	poller := NewServiceHealthPoller(
		cache,
		func() (*ServiceHealthConfig, error) { return config, nil },
		func(config *ServiceHealthConfig) serviceHealthProber {
			if newProber == nil {
				return nil
			}
			return newProber(cfg, config)
		},
		newDoctorDiscardLogger(),
	)
	cycleLimit := config.Interval
	if cycleLimit > 30*time.Second {
		cycleLimit = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), cycleLimit)
	defer cancel()
	poller.runCycle(ctx)

	counts := make(map[ServiceHealthState]int)
	for _, service := range config.Services {
		result, ok := poller.Store().Get(service.ProxyHostID)
		if !ok {
			result = ServiceHealthResult{ProxyHostID: service.ProxyHostID, State: ServiceHealthStateUnknown}
		}
		counts[result.State]++
		prefix := "WARN"
		if result.State == ServiceHealthStateReachable {
			prefix = "PASS"
		}
		statusClass := ""
		if result.HTTPStatusClass > 0 {
			statusClass = fmt.Sprintf(" status_class=%dxx", result.HTTPStatusClass)
		}
		fmt.Fprintf(
			stdout,
			"%s service health target %d (%s): %s duration=%s%s\n",
			prefix,
			service.ProxyHostID,
			service.Type,
			result.State,
			result.Duration.Round(time.Millisecond),
			statusClass,
		)
	}
	fmt.Fprintf(
		stdout,
		"PASS service health summary: configured=%d reachable=%d auth_required=%d response_error=%d unreachable=%d unknown=%d\n",
		len(config.Services),
		counts[ServiceHealthStateReachable],
		counts[ServiceHealthStateAuthRequired],
		counts[ServiceHealthStateResponseError],
		counts[ServiceHealthStateUnreachable],
		counts[ServiceHealthStateUnknown],
	)
}

func newDoctorDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func snapshotStageFailure(err error) (string, error) {
	var loadErr *snapshotLoadError
	if errors.As(err, &loadErr) {
		return string(loadErr.Stage), loadErr.Err
	}
	return "snapshot", err
}

func doctorSecretValues(lookup configLookup) []string {
	if lookup == nil {
		return nil
	}
	values := make([]string, 0, 7)
	for _, key := range []string{
		"HEADSCALE_URL",
		"HEADSCALE_API_KEY",
		"TAILSCALE_OAUTH_CLIENT_ID",
		"TAILSCALE_OAUTH_CLIENT_SECRET",
		"NPM_URL",
		"NPM_EMAIL",
		"NPM_PASSWORD",
	} {
		if value, ok, err := lookup(key); err == nil && ok && value != "" {
			values = append(values, value)
		}
	}
	return values
}

func doctorControlPlaneToken(controlPlane ControlPlane) string {
	client, ok := controlPlane.(*TailscaleClient)
	if !ok || client == nil {
		return ""
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.accessToken
}

func doctorNPMToken(client *NPMClient) string {
	if client == nil {
		return ""
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.token
}

func sanitizeDoctorError(err error, secrets []string) string {
	if err == nil {
		return "unknown error"
	}

	text := err.Error()
	redactions := append([]string(nil), secrets...)
	sort.Slice(redactions, func(i, j int) bool {
		return len(redactions[i]) > len(redactions[j])
	})
	for _, secret := range redactions {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
		if encoded, marshalErr := json.Marshal(secret); marshalErr == nil && len(encoded) >= 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
	}

	working := []rune(text)
	if len(working) > maxDoctorErrorWork {
		working = working[:maxDoctorErrorWork]
	}
	text = string(working)
	text = doctorHTTPBodyRE.ReplaceAllString(text, `${1}: [REDACTED]`)
	text = doctorSensitiveFieldRE.ReplaceAllString(text, `${1}[REDACTED]`)
	text = doctorBearerRE.ReplaceAllString(text, `${1}[REDACTED]`)
	text = doctorJWTRE.ReplaceAllString(text, `[REDACTED]`)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "request failed without details"
	}

	bounded := []rune(text)
	if len(bounded) > maxDoctorErrorLength {
		bounded = append(bounded[:maxDoctorErrorLength-3], '.', '.', '.')
	}
	return string(bounded)
}

type unmatchedDoctorJoin struct {
	domain      string
	forwardHost string
}

func reportDoctorJoinCoverage(writer io.Writer, snapshot *CacheData) {
	matched, total, unmatched := doctorJoinCoverage(snapshot)
	switch {
	case total == 0:
		fmt.Fprintln(writer, "WARN supported join coverage: no enabled proxy hosts with domains to evaluate")
	case matched == total:
		fmt.Fprintf(writer, "PASS supported join coverage: %d/%d enabled proxy hosts match a supported access-rule destination\n", matched, total)
	default:
		fmt.Fprintf(writer, "WARN supported join coverage: %d/%d enabled proxy hosts match a supported access-rule destination\n", matched, total)
	}
	for _, pair := range unmatched {
		fmt.Fprintf(writer, "WARN unmatched join: %q -> %q\n", pair.domain, pair.forwardHost)
	}
}

func doctorJoinCoverage(snapshot *CacheData) (matched int, total int, unmatched []unmatchedDoctorJoin) {
	if snapshot == nil || snapshot.Policy == nil {
		return 0, 0, nil
	}

	tagIPs := make(map[string][]string)
	var allNodeIPs []string
	for _, node := range snapshot.Nodes {
		allNodeIPs = append(allNodeIPs, node.Addresses...)
		for _, tag := range nodeTags(node) {
			tagIPs[tag] = append(tagIPs[tag], node.Addresses...)
		}
	}
	matchData := &matchContext{
		hosts:   snapshot.Policy.Hosts,
		tagIPs:  tagIPs,
		selfIPs: allNodeIPs,
	}

	for _, proxyHost := range snapshot.ProxyHosts {
		if !proxyHost.Enabled || len(proxyHost.DomainNames) == 0 {
			continue
		}
		total++
		if doctorProxyHostHasSupportedJoin(proxyHost, snapshot.Policy, matchData) {
			matched++
			continue
		}
		for _, domain := range proxyHost.DomainNames {
			unmatched = append(unmatched, unmatchedDoctorJoin{domain: domain, forwardHost: proxyHost.ForwardHost})
		}
	}

	sort.Slice(unmatched, func(i, j int) bool {
		leftDomain := strings.ToLower(unmatched[i].domain)
		rightDomain := strings.ToLower(unmatched[j].domain)
		if leftDomain != rightDomain {
			return leftDomain < rightDomain
		}
		if unmatched[i].domain != unmatched[j].domain {
			return unmatched[i].domain < unmatched[j].domain
		}
		return unmatched[i].forwardHost < unmatched[j].forwardHost
	})
	return matched, total, unmatched
}

func doctorProxyHostHasSupportedJoin(proxyHost ProxyHost, policy *Policy, matchData *matchContext) bool {
	for _, rule := range policy.accessRules() {
		if !rule.permitsTCP(proxyHost.ForwardPort) {
			continue
		}
		for _, destination := range rule.Dst {
			if matchDst(stripPort(destination), proxyHost.ForwardHost, matchData) {
				return true
			}
		}
	}
	return false
}

func reportDoctorSingleAddressCIDR(writer io.Writer, label, value string, network *net.IPNet) {
	ones, bits := network.Mask.Size()
	if ones == bits {
		fmt.Fprintf(writer, "PASS %s: %s contains one address\n", label, value)
		return
	}
	fmt.Fprintf(writer, "WARN %s: %s contains multiple addresses; confirm every source may assert identity\n", label, value)
}

func pluralDoctorNoun(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func reportDoctorIdentityPreviews(writer io.Writer, identities []string, snapshot *CacheData) {
	if len(identities) == 0 {
		fmt.Fprintln(writer, "WARN identity previews: none requested; use --identity LOGIN to preview cards")
		return
	}

	machinesAvailable := machineProjectionAvailable(snapshot)
	for _, login := range identities {
		identity := &Identity{Login: login}
		cards := MatchServices(identity, snapshot)
		if len(cards) == 0 {
			fmt.Fprintf(writer, "WARN identity preview %q: 0 cards from the supported matcher\n", login)
		} else {
			fmt.Fprintf(writer, "PASS identity preview %q: %d %s from the supported matcher\n", login, len(cards), pluralDoctorNoun(len(cards), "card", "cards"))
			for _, card := range cards {
				fmt.Fprintf(
					writer,
					"  CARD %q -> %q (link_state=%s npm_route_online=%t)\n",
					card.Domain,
					card.URL,
					card.LinkState,
					npmRouteOnline(snapshot, card.ID),
				)
			}
		}

		if !machinesAvailable {
			continue
		}
		machines := MatchMachines(identity, snapshot)
		if len(machines) == 0 {
			fmt.Fprintf(writer, "WARN machine preview %q: 0 machines from SSH policy, Grant TCP/22, and reported SSH-capable device evidence\n", login)
			continue
		}
		fmt.Fprintf(
			writer,
			"PASS machine preview %q: %d %s from SSH policy, Grant TCP/22, and reported SSH-capable device evidence; targets and accounts omitted\n",
			login,
			len(machines),
			pluralDoctorNoun(len(machines), "machine", "machines"),
		)
	}
}

func npmRouteOnline(snapshot *CacheData, proxyHostID int) bool {
	if snapshot == nil {
		return false
	}
	for _, proxyHost := range snapshot.ProxyHosts {
		if proxyHost.ID == proxyHostID {
			return proxyHost.Meta.NginxOnline
		}
	}
	return false
}
