package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

const doctorUsage = `Usage:
  velociportal doctor [--env-file FILE] [--identity LOGIN ...]

Options:
  --env-file FILE    Load configuration only from FILE instead of process environment
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
	newClients func(*Config) (*HeadscaleClient, *NPMClient)
}

func defaultDoctorDependencies() doctorDependencies {
	return doctorDependencies{newClients: newUpstreamClients}
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
	var identities doctorIdentityFlags
	flags.Func("env-file", "load configuration from file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
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

	secrets := doctorSecretValues(lookup)
	cfg, err := loadConfigFrom(lookup)
	if err != nil {
		fmt.Fprintf(stdout, "FAIL config: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	secrets = append(secrets, cfg.HeadscaleAPIKey, cfg.NPMPassword)
	fmt.Fprintln(stdout, "PASS config: required values validated")

	ones, bits := cfg.TrustedProxyCIDR.Mask.Size()
	if ones == bits {
		fmt.Fprintf(stdout, "PASS trusted proxy CIDR: %s contains one address\n", cfg.TrustedProxyCIDR.String())
	} else {
		fmt.Fprintf(stdout, "WARN trusted proxy CIDR: %s contains multiple addresses; confirm every source may assert identity\n", cfg.TrustedProxyCIDR.String())
	}
	if classifyHeadscaleTransport(cfg.HeadscaleURL) == headscaleTransportRestrictedHTTP {
		fmt.Fprintln(stdout, headscaleHTTPDoctorWarning)
	}

	if dependencies.newClients == nil {
		dependencies.newClients = newUpstreamClients
	}
	headscale, npm := dependencies.newClients(cfg)

	progress := func(stage snapshotLoadStage, count int) {
		switch stage {
		case snapshotStageHeadscalePolicy:
			fmt.Fprintf(stdout, "PASS Headscale policy: loaded %d ACL rules\n", count)
		case snapshotStageHeadscaleNodes:
			fmt.Fprintf(stdout, "PASS Headscale nodes: loaded %d nodes\n", count)
		case snapshotStageNPMAuth:
			fmt.Fprintln(stdout, "PASS NPM authentication: credentials accepted")
		case snapshotStageNPMProxyHosts:
			fmt.Fprintf(stdout, "PASS NPM proxy hosts: loaded %d proxy hosts\n", count)
		}
	}

	snapshot, err := loadSnapshotWithProgress(context.Background(), headscale, npm, progress)
	if err != nil {
		stage, cause := snapshotStageFailure(err)
		secrets = append(secrets, doctorNPMToken(npm))
		fmt.Fprintf(stdout, "FAIL %s: %s\n", stage, sanitizeDoctorError(cause, secrets))
		fmt.Fprintf(stdout, "FAIL snapshot: not created because %s failed\n", stage)
		return 1
	}
	if snapshot == nil || snapshot.Policy == nil {
		fmt.Fprintln(stdout, "FAIL snapshot: loader returned an incomplete snapshot")
		return 1
	}
	fmt.Fprintf(stdout, "PASS snapshot: complete (%d ACL rules, %d nodes, %d proxy hosts)\n", len(snapshot.Policy.ACLs), len(snapshot.Nodes), len(snapshot.ProxyHosts))

	reportDoctorJoinCoverage(stdout, snapshot)
	reportDoctorIdentityPreviews(stdout, identities, snapshot)
	fmt.Fprintln(stdout, "WARN validation scope: join and card results are supported matcher previews, not proof of network authorization or reachability")
	fmt.Fprintln(stdout, "PASS doctor: required diagnostics completed")
	return 0
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
	values := make([]string, 0, 2)
	for _, key := range []string{"HEADSCALE_API_KEY", "NPM_PASSWORD"} {
		if value, ok, err := lookup(key); err == nil && ok && value != "" {
			values = append(values, value)
		}
	}
	return values
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
		fmt.Fprintf(writer, "PASS supported join coverage: %d/%d enabled proxy hosts match a supported ACL destination\n", matched, total)
	default:
		fmt.Fprintf(writer, "WARN supported join coverage: %d/%d enabled proxy hosts match a supported ACL destination\n", matched, total)
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
		allNodeIPs = append(allNodeIPs, node.IPAddresses...)
		for _, tag := range nodeTags(node) {
			tagIPs[tag] = append(tagIPs[tag], node.IPAddresses...)
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
		if doctorProxyHostHasSupportedJoin(proxyHost.ForwardHost, snapshot.Policy.ACLs, matchData) {
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

func doctorProxyHostHasSupportedJoin(forwardHost string, rules []ACLRule, matchData *matchContext) bool {
	for _, rule := range rules {
		if rule.Action != "accept" {
			continue
		}
		for _, destination := range rule.Dst {
			if matchDst(stripPort(destination), forwardHost, matchData) {
				return true
			}
		}
	}
	return false
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

	for _, login := range identities {
		cards := MatchServices(&Identity{Login: login}, snapshot)
		if len(cards) == 0 {
			fmt.Fprintf(writer, "WARN identity preview %q: 0 cards from the supported matcher\n", login)
			continue
		}
		fmt.Fprintf(writer, "PASS identity preview %q: %d %s from the supported matcher\n", login, len(cards), pluralDoctorNoun(len(cards), "card", "cards"))
		for _, card := range cards {
			fmt.Fprintf(writer, "  CARD %q -> %q (npm_online=%t)\n", card.Domain, card.URL, card.Online)
		}
	}
}
