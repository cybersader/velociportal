package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

const validationUsage = `Usage:
  velociportal validate --identity LABEL=LOGIN --identity LABEL=LOGIN [options]

Options:
  --env-file FILE       Load configuration only from FILE instead of process environment
  --identity LABEL=LOGIN
                        Label and evaluate an identity; required at least twice, maximum 20
  --format FORMAT       Report format: text or json (default: text)
  --privacy MODE        Output detail: summary or private (default: summary)
  -h, --help            Show this help
`

const (
	validationSchemaVersion              = "2"
	maxValidationIdentities              = 20
	maxValidationLabelBytes              = 64
	validationScopeNotice                = "Matcher evidence only: this report does not prove selected control-plane authorization, proxy identity injection, service reachability, or link correctness."
	headscaleHTTPValidationScopeNotice   = "For Headscale HTTP, this report does not prove private Docker/host route confinement or external inaccessibility."
	headscaleHTTPValidationWarning       = "WARNING: Headscale HTTP route confinement and external inaccessibility are not proven."
	headscaleHTTPRouteUnverifiedCode     = "headscale-http-route-unverified"
	headscaleHTTPRouteUnverifiedMessage  = "The restricted Headscale HTTP path is configured; private Docker/host route confinement and external inaccessibility remain unverified."
	implicitControlPlaneSelectionCode    = "implicit-control-plane-selection"
	implicitControlPlaneSelectionMessage = "CONTROL_PLANE is implicit Headscale compatibility behavior for v0.2; set CONTROL_PLANE=headscale before v0.3."
)

type validationFormat string

type validationPrivacy string

const (
	validationFormatText validationFormat = "text"
	validationFormatJSON validationFormat = "json"

	validationPrivacySummary validationPrivacy = "summary"
	validationPrivacyPrivate validationPrivacy = "private"
)

type validationIdentityInput struct {
	Label string
	Login string
}

type validationIdentityFlags []validationIdentityInput

func (values *validationIdentityFlags) String() string {
	labels := make([]string, len(*values))
	for index, value := range *values {
		labels[index] = value.Label
	}
	return strings.Join(labels, ",")
}

func (values *validationIdentityFlags) Set(raw string) error {
	if len(*values) >= maxValidationIdentities {
		return fmt.Errorf("at most %d identities may be evaluated", maxValidationIdentities)
	}
	separator := strings.IndexByte(raw, '=')
	if separator <= 0 || separator == len(raw)-1 {
		return errors.New("identity must use LABEL=LOGIN")
	}
	label := strings.TrimSpace(raw[:separator])
	login := strings.TrimSpace(raw[separator+1:])
	if err := validateValidationLabel(label); err != nil {
		return err
	}
	if login == "" {
		return errors.New("identity login must not be empty")
	}
	if strings.ContainsAny(login, "\r\n\x00") {
		return errors.New("identity login must not contain CR, LF, or NUL")
	}
	if len([]rune(login)) > maxDoctorIdentityLength {
		return fmt.Errorf("identity login exceeds %d characters", maxDoctorIdentityLength)
	}
	for _, existing := range *values {
		if existing.Label == label {
			return fmt.Errorf("duplicate identity label %q", label)
		}
		if existing.Login == login {
			return fmt.Errorf("identity login is already assigned to label %q", existing.Label)
		}
	}
	*values = append(*values, validationIdentityInput{Label: label, Login: login})
	return nil
}

func validateValidationLabel(label string) error {
	if label == "" {
		return errors.New("identity label must not be empty")
	}
	if len(label) > maxValidationLabelBytes {
		return fmt.Errorf("identity label exceeds %d bytes", maxValidationLabelBytes)
	}
	for index := 0; index < len(label); index++ {
		character := label[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return fmt.Errorf("identity label %q contains unsupported characters", label)
	}
	return nil
}

type ValidationReport struct {
	SchemaVersion  string                     `json:"schema_version"`
	GeneratedAt    time.Time                  `json:"generated_at"`
	Status         string                     `json:"status"`
	Scope          string                     `json:"scope"`
	Privacy        string                     `json:"privacy"`
	ConfigSource   string                     `json:"config_source"`
	ControlPlane   ValidationControlPlane     `json:"control_plane"`
	Build          BuildInfo                  `json:"build"`
	Snapshot       ValidationSnapshot         `json:"snapshot"`
	Services       []ValidationService        `json:"services"`
	Identities     []ValidationIdentityReport `json:"identities"`
	CommonServices []string                   `json:"common_services"`
	Findings       []ValidationFinding        `json:"findings"`
}

type ValidationControlPlane struct {
	Provider     controlPlaneProvider     `json:"provider"`
	PolicyMode   string                   `json:"policy_mode"`
	SupportLevel controlPlaneSupportLevel `json:"support_level"`
	Selection    string                   `json:"selection"`
}

type ValidationSnapshot struct {
	ACLRules          int `json:"acl_rules"`
	Nodes             int `json:"nodes"`
	ProxyHosts        int `json:"proxy_hosts"`
	EnabledProxyHosts int `json:"enabled_proxy_hosts"`
	EvaluatedServices int `json:"evaluated_services"`
}

type ValidationService struct {
	ID                   string                    `json:"id"`
	ForwardHostClass     string                    `json:"forward_host_class"`
	StructuralMatchKinds []destinationMatchKind    `json:"structural_match_kinds"`
	StructuralMatchPaths int                       `json:"structural_match_paths"`
	IdentityDependent    bool                      `json:"identity_dependent"`
	VisibleTo            []string                  `json:"visible_to"`
	Private              *ValidationPrivateService `json:"private,omitempty"`
}

type ValidationPrivateService struct {
	ProxyHostID   int      `json:"proxy_host_id"`
	Domains       []string `json:"domains"`
	ForwardScheme string   `json:"forward_scheme"`
	ForwardHost   string   `json:"forward_host"`
	ForwardPort   int      `json:"forward_port"`
	CardURL       string   `json:"card_url"`
}

type ValidationIdentityReport struct {
	Label          string                      `json:"label"`
	Services       []ValidationIdentityService `json:"services"`
	UniqueServices []string                    `json:"unique_services"`
}

type ValidationIdentityService struct {
	ServiceID       string                  `json:"service_id"`
	ACLIndex        int                     `json:"acl_index"`
	DestinationKind destinationMatchKind    `json:"destination_kind"`
	Private         *ValidationPrivateMatch `json:"private,omitempty"`
}

type ValidationPrivateMatch struct {
	SourceToken         string `json:"source_token"`
	DestinationSelector string `json:"destination_selector"`
	ResolvedValue       string `json:"resolved_value"`
}

type ValidationFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

type validationDependencies struct {
	now          func() time.Time
	loadSnapshot func(context.Context, ControlPlane, *NPMClient) (*CacheData, error)
}

func defaultValidationDependencies() validationDependencies {
	return validationDependencies{
		now: time.Now,
		loadSnapshot: func(ctx context.Context, controlPlane ControlPlane, npm *NPMClient) (*CacheData, error) {
			return loadSnapshot(ctx, controlPlane, npm)
		},
	}
}

func runValidationCommand(args []string, stdout, stderr io.Writer) int {
	return runValidationCommandWithDependencies(args, stdout, stderr, defaultValidationDependencies())
}

func runValidationCommandWithDependencies(args []string, stdout, stderr io.Writer, dependencies validationDependencies) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, validationUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: validate: %s cannot be combined with other arguments\n\n%s", arg, validationUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var envFile string
	var identities validationIdentityFlags
	formatText := string(validationFormatText)
	privacyText := string(validationPrivacySummary)
	flags.Func("env-file", "load configuration from file", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("path must not be empty")
		}
		envFile = value
		return nil
	})
	flags.Var(&identities, "identity", "label and evaluate an identity")
	flags.StringVar(&formatText, "format", formatText, "report format")
	flags.StringVar(&privacyText, "privacy", privacyText, "privacy mode")
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: validate: %s\n\n%s", safeValidationFlagError(err), validationUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: validate does not accept positional arguments\n\n%s", validationUsage)
		return 2
	}
	if len(identities) < 2 {
		fmt.Fprintf(stderr, "velociportal: validate requires at least two --identity LABEL=LOGIN values\n\n%s", validationUsage)
		return 2
	}
	format := validationFormat(strings.ToLower(strings.TrimSpace(formatText)))
	if format != validationFormatText && format != validationFormatJSON {
		fmt.Fprintf(stderr, "velociportal: validate: --format must be text or json\n\n%s", validationUsage)
		return 2
	}
	privacy := validationPrivacy(strings.ToLower(strings.TrimSpace(privacyText)))
	if privacy != validationPrivacySummary && privacy != validationPrivacyPrivate {
		fmt.Fprintf(stderr, "velociportal: validate: --privacy must be summary or private\n\n%s", validationUsage)
		return 2
	}

	lookup, configSource, secrets, err := validationConfigLookup(envFile)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: validate: %s\n", sanitizeDoctorError(err, nil))
		return 1
	}
	cfg, err := loadConfigFrom(lookup)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: validate: %s\n", sanitizeDoctorError(err, secrets))
		return 1
	}
	secrets = append(secrets, cfg.ControlPlaneRedactionValues...)
	secrets = append(secrets, cfg.HeadscaleAPIKey, cfg.TailscaleOAuthClientID, cfg.TailscaleOAuthClientSecret, cfg.NPMPassword)
	if !cfg.ControlPlaneExplicit {
		fmt.Fprintln(stderr, implicitHeadscaleDeprecationWarning)
	}
	if len(cfg.InactiveControlPlaneKeys) > 0 {
		fmt.Fprintf(stderr, "WARNING: inactive control-plane configuration is ignored: %s\n", strings.Join(cfg.InactiveControlPlaneKeys, ", "))
	}
	headscaleHTTP := cfg.ControlPlane == controlPlaneHeadscale && classifyHeadscaleTransport(cfg.HeadscaleURL) == headscaleTransportRestrictedHTTP
	if headscaleHTTP {
		fmt.Fprintln(stderr, headscaleHTTPValidationWarning)
	}
	if dependencies.now == nil {
		dependencies.now = time.Now
	}
	if dependencies.loadSnapshot == nil {
		dependencies.loadSnapshot = defaultValidationDependencies().loadSnapshot
	}

	controlPlane, npm := newUpstreamClients(cfg)
	snapshot, err := dependencies.loadSnapshot(context.Background(), controlPlane, npm)
	if err != nil {
		secrets = append(secrets, doctorControlPlaneToken(controlPlane), doctorNPMToken(npm))
		fmt.Fprintf(stderr, "velociportal: validate: %s\n", sanitizeValidationRuntimeError(err, privacy, secrets))
		return 1
	}

	report := buildValidationReport(snapshot, identities, privacy, configSource, dependencies.now().UTC())
	report.ControlPlane.Selection = "explicit"
	if !cfg.ControlPlaneExplicit {
		report.ControlPlane.Selection = "implicit"
		addValidationFinding(&report, "notice", implicitControlPlaneSelectionCode, "", implicitControlPlaneSelectionMessage)
		sortValidationFindings(&report)
	}
	if headscaleHTTP {
		addHeadscaleHTTPValidationNotice(&report)
	}
	if privacy == validationPrivacyPrivate {
		fmt.Fprintln(stderr, "WARNING: private validation output contains internal topology and must not be shared publicly.")
	}
	if err := renderValidationReport(stdout, report, format); err != nil {
		fmt.Fprintf(stderr, "velociportal: validate: render report: %v\n", err)
		return 1
	}
	if report.Status != "pass" {
		return 1
	}
	return 0
}

func sanitizeValidationRuntimeError(err error, privacy validationPrivacy, secrets []string) string {
	if privacy == validationPrivacySummary {
		var unsupported *unsupportedPolicyError
		if errors.As(err, &unsupported) {
			if unsupported.Section == "" {
				return "selected control-plane policy uses unsupported access-control semantics"
			}
			return fmt.Sprintf("selected control-plane policy section %q uses unsupported access-control semantics", unsupported.Section)
		}
	}
	return sanitizeDoctorError(err, secrets)
}

func safeValidationFlagError(err error) string {
	if err == nil {
		return "invalid command arguments"
	}
	if strings.Contains(err.Error(), "for flag -identity") {
		return "invalid --identity value; use LABEL=LOGIN with unique labels and logins"
	}
	return err.Error()
}

func validationConfigLookup(envFile string) (configLookup, string, []string, error) {
	if envFile == "" {
		lookup := configLookup(processConfigLookup)
		return lookup, "process_environment", doctorSecretValues(lookup), nil
	}
	values, err := readEnvFile(envFile)
	if err != nil {
		return nil, "", nil, err
	}
	info, err := os.Stat(envFile)
	if err != nil {
		return nil, "", nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, "", nil, fmt.Errorf("configuration path is not a regular file")
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		return nil, "", nil, fmt.Errorf("environment file permissions %04o allow group or other access; require owner-only permissions", permissions)
	}
	lookup := mapConfigLookup(values)
	return lookup, "environment_file", doctorSecretValues(lookup), nil
}

func buildValidationReport(snapshot *CacheData, identities []validationIdentityInput, privacy validationPrivacy, configSource string, generatedAt time.Time) ValidationReport {
	report := ValidationReport{
		SchemaVersion: validationSchemaVersion,
		GeneratedAt:   generatedAt,
		Status:        "pass",
		Scope:         validationScopeNotice,
		Privacy:       string(privacy),
		ConfigSource:  configSource,
		ControlPlane: ValidationControlPlane{
			Selection: "unknown",
		},
		Build:          currentBuildInfo(),
		Services:       []ValidationService{},
		Identities:     []ValidationIdentityReport{},
		CommonServices: []string{},
		Findings:       []ValidationFinding{},
	}
	if snapshot != nil {
		report.ControlPlane.Provider = snapshot.ControlPlane.Provider
		report.ControlPlane.PolicyMode = snapshot.ControlPlane.PolicyMode
		report.ControlPlane.SupportLevel = snapshot.ControlPlane.SupportLevel
	}
	if snapshot == nil || snapshot.Policy == nil {
		report.Status = "review_required"
		report.Findings = append(report.Findings, ValidationFinding{Severity: "review", Code: "incomplete-snapshot", Message: "Snapshot is incomplete."})
		return report
	}
	report.Snapshot.ACLRules = len(snapshot.Policy.ACLs)
	report.Snapshot.Nodes = len(snapshot.Nodes)
	report.Snapshot.ProxyHosts = len(snapshot.ProxyHosts)
	if !report.Build.SourceTraceable() {
		addValidationFinding(&report, "review", "untraceable-build", "", "Build revision is unknown or the source tree was not clean.")
	}

	proxyHosts := validationProxyHosts(snapshot.ProxyHosts)
	report.Snapshot.EvaluatedServices = len(proxyHosts)
	enabledWithoutDomains := 0
	for _, proxyHost := range snapshot.ProxyHosts {
		if proxyHost.Enabled {
			report.Snapshot.EnabledProxyHosts++
			if len(proxyHost.DomainNames) == 0 {
				enabledWithoutDomains++
			}
		}
	}
	if enabledWithoutDomains > 0 {
		addValidationFinding(&report, "review", "enabled-host-without-domain", "", fmt.Sprintf("%d enabled NPM proxy host(s) have no domain to evaluate.", enabledWithoutDomains))
	}

	serviceIDs := make(map[int]string, len(proxyHosts))
	serviceIndexes := make(map[int]int, len(proxyHosts))
	for index, proxyHost := range proxyHosts {
		serviceID := fmt.Sprintf("service-%03d", index+1)
		serviceIDs[proxyHost.ID] = serviceID
		serviceIndexes[proxyHost.ID] = index
		matches, identityDependent := structuralValidationMatches(proxyHost, snapshot)
		kinds := uniqueDestinationKinds(matches)
		service := ValidationService{
			ID:                   serviceID,
			ForwardHostClass:     classifyForwardHost(proxyHost.ForwardHost),
			StructuralMatchKinds: kinds,
			StructuralMatchPaths: len(matches),
			IdentityDependent:    identityDependent,
			VisibleTo:            []string{},
		}
		if privacy == validationPrivacyPrivate {
			scheme := proxyHost.ForwardScheme
			if scheme == "" {
				scheme = "https"
			}
			service.Private = &ValidationPrivateService{
				ProxyHostID:   proxyHost.ID,
				Domains:       append([]string(nil), proxyHost.DomainNames...),
				ForwardScheme: proxyHost.ForwardScheme,
				ForwardHost:   proxyHost.ForwardHost,
				ForwardPort:   proxyHost.ForwardPort,
				CardURL:       scheme + "://" + proxyHost.DomainNames[0],
			}
		}
		report.Services = append(report.Services, service)
		if len(proxyHost.DomainNames) > 1 {
			addValidationFinding(&report, "notice", "additional-domains-not-rendered", serviceID, "Only the first NPM domain currently becomes a card.")
		}
		if len(matches) > 1 {
			addValidationFinding(&report, "notice", "multiple-structural-match-paths", serviceID, "The forward target matches more than one supported ACL destination path.")
		}
	}
	if len(proxyHosts) > 0 {
		addValidationFinding(&report, "notice", "browser-scheme-unverified", "", "Card URLs currently reuse NPM backend schemes; verify every public URL manually.")
	}

	identitySets := make(map[string]map[string]bool, len(identities))
	for _, identityInput := range identities {
		matches := evaluateServices(&Identity{Login: identityInput.Login}, snapshot)
		identityReport := ValidationIdentityReport{Label: identityInput.Label, Services: []ValidationIdentityService{}, UniqueServices: []string{}}
		set := make(map[string]bool)
		for _, match := range matches {
			serviceID, exists := serviceIDs[match.ProxyHost.ID]
			if !exists {
				continue
			}
			entry := ValidationIdentityService{
				ServiceID:       serviceID,
				ACLIndex:        match.ACLIndex,
				DestinationKind: match.Destination.Kind,
			}
			if privacy == validationPrivacyPrivate {
				entry.Private = &ValidationPrivateMatch{
					SourceToken:         privateValidationSourceToken(match.SourceToken, identityInput.Login),
					DestinationSelector: match.Destination.Selector,
					ResolvedValue:       match.Destination.ResolvedValue,
				}
			}
			identityReport.Services = append(identityReport.Services, entry)
			set[serviceID] = true
			serviceIndex := serviceIndexes[match.ProxyHost.ID]
			report.Services[serviceIndex].VisibleTo = append(report.Services[serviceIndex].VisibleTo, identityInput.Label)
		}
		sort.Slice(identityReport.Services, func(i, j int) bool {
			return identityReport.Services[i].ServiceID < identityReport.Services[j].ServiceID
		})
		if len(identityReport.Services) == 0 {
			addValidationFinding(&report, "review", "zero-card-identity", identityInput.Label, "Identity receives no cards from the supported matcher.")
		}
		identitySets[identityInput.Label] = set
		report.Identities = append(report.Identities, identityReport)
	}

	for index := range report.Services {
		sort.Strings(report.Services[index].VisibleTo)
		if len(report.Services[index].StructuralMatchKinds) == 0 && len(report.Services[index].VisibleTo) == 0 {
			addValidationFinding(&report, "review", "unmatched-forward-host", report.Services[index].ID, "No supplied identity or identity-independent supported destination matches this forward target.")
		}
	}

	report.CommonServices = commonValidationServices(identitySets)
	for index := range report.Identities {
		label := report.Identities[index].Label
		report.Identities[index].UniqueServices = uniqueValidationServices(label, identitySets)
	}
	if validationSetsIdentical(identitySets) {
		addValidationFinding(&report, "review", "identical-card-sets", "", "All supplied identities receive identical card sets; confirm that their policy membership is meaningfully different.")
	}

	sortValidationFindings(&report)
	return report
}

func validationProxyHosts(proxyHosts []ProxyHost) []ProxyHost {
	result := make([]ProxyHost, 0, len(proxyHosts))
	for _, proxyHost := range proxyHosts {
		if proxyHost.Enabled && len(proxyHost.DomainNames) > 0 {
			result = append(result, proxyHost)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		leftDomain := strings.ToLower(result[i].DomainNames[0])
		rightDomain := strings.ToLower(result[j].DomainNames[0])
		if leftDomain != rightDomain {
			return leftDomain < rightDomain
		}
		return result[i].ForwardHost < result[j].ForwardHost
	})
	return result
}

func structuralValidationMatches(proxyHost ProxyHost, snapshot *CacheData) ([]destinationMatchEvidence, bool) {
	tagIPs := make(map[string][]string)
	for _, node := range snapshot.Nodes {
		for _, tag := range nodeTags(node) {
			tagIPs[tag] = append(tagIPs[tag], node.Addresses...)
		}
	}
	context := &matchContext{hosts: snapshot.Policy.Hosts, tagIPs: tagIPs}
	matches := []destinationMatchEvidence{}
	identityDependent := false
	for _, acl := range snapshot.Policy.ACLs {
		if acl.Action != "accept" {
			continue
		}
		for _, selector := range acl.Dst {
			if stripPort(selector) == "autogroup:self" {
				identityDependent = true
				continue
			}
			if evidence, matched := matchDestination(selector, proxyHost.ForwardHost, context); matched {
				matches = append(matches, evidence)
			}
		}
	}
	return matches, identityDependent
}

func uniqueDestinationKinds(matches []destinationMatchEvidence) []destinationMatchKind {
	seen := make(map[destinationMatchKind]bool)
	for _, match := range matches {
		seen[match.Kind] = true
	}
	result := make([]destinationMatchKind, 0, len(seen))
	for kind := range seen {
		result = append(result, kind)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func privateValidationSourceToken(source, login string) string {
	if source == "*" || strings.HasPrefix(source, "group:") {
		return source
	}
	if identityTokens(login)[source] {
		return "identity"
	}
	return source
}

func classifyForwardHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "empty"
	}
	if net.ParseIP(host) != nil {
		return "ip"
	}
	if _, _, err := net.ParseCIDR(host); err == nil {
		return "cidr"
	}
	if strings.EqualFold(host, "localhost") {
		return "localhost"
	}
	if strings.Contains(host, ".") {
		return "fqdn"
	}
	if strings.ContainsAny(host, " /\\") {
		return "other"
	}
	return "short_name"
}

func commonValidationServices(identitySets map[string]map[string]bool) []string {
	if len(identitySets) == 0 {
		return []string{}
	}
	labels := make([]string, 0, len(identitySets))
	for label := range identitySets {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	result := []string{}
	for serviceID := range identitySets[labels[0]] {
		common := true
		for _, label := range labels[1:] {
			if !identitySets[label][serviceID] {
				common = false
				break
			}
		}
		if common {
			result = append(result, serviceID)
		}
	}
	sort.Strings(result)
	return result
}

func uniqueValidationServices(label string, identitySets map[string]map[string]bool) []string {
	result := []string{}
	for serviceID := range identitySets[label] {
		unique := true
		for otherLabel, set := range identitySets {
			if otherLabel != label && set[serviceID] {
				unique = false
				break
			}
		}
		if unique {
			result = append(result, serviceID)
		}
	}
	sort.Strings(result)
	return result
}

func validationSetsIdentical(identitySets map[string]map[string]bool) bool {
	if len(identitySets) < 2 {
		return false
	}
	labels := make([]string, 0, len(identitySets))
	for label := range identitySets {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	baseline := identitySets[labels[0]]
	for _, label := range labels[1:] {
		candidate := identitySets[label]
		if len(candidate) != len(baseline) {
			return false
		}
		for serviceID := range baseline {
			if !candidate[serviceID] {
				return false
			}
		}
	}
	return true
}

func addHeadscaleHTTPValidationNotice(report *ValidationReport) {
	if report == nil {
		return
	}
	report.Scope = strings.TrimSpace(report.Scope + " " + headscaleHTTPValidationScopeNotice)
	addValidationFinding(report, "notice", headscaleHTTPRouteUnverifiedCode, "", headscaleHTTPRouteUnverifiedMessage)
	sortValidationFindings(report)
}

func addValidationFinding(report *ValidationReport, severity, code, subject, message string) {
	report.Findings = append(report.Findings, ValidationFinding{Severity: severity, Code: code, Subject: subject, Message: message})
	if severity == "review" {
		report.Status = "review_required"
	}
}

func sortValidationFindings(report *ValidationReport) {
	sort.Slice(report.Findings, func(i, j int) bool {
		leftRank := validationSeverityRank(report.Findings[i].Severity)
		rightRank := validationSeverityRank(report.Findings[j].Severity)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if report.Findings[i].Code != report.Findings[j].Code {
			return report.Findings[i].Code < report.Findings[j].Code
		}
		return report.Findings[i].Subject < report.Findings[j].Subject
	})
}

func validationSeverityRank(severity string) int {
	if severity == "review" {
		return 0
	}
	return 1
}

func renderValidationReport(writer io.Writer, report ValidationReport, format validationFormat) error {
	if format == validationFormatJSON {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	return renderValidationText(writer, report)
}

func renderValidationText(writer io.Writer, report ValidationReport) error {
	var output strings.Builder
	fmt.Fprintf(&output, "Velociportal validation report %s\n", report.SchemaVersion)
	fmt.Fprintf(&output, "STATUS %s\n", strings.ToUpper(report.Status))
	fmt.Fprintf(&output, "Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(
		&output,
		"Control plane: provider=%s policy_mode=%s support_level=%s selection=%s\n",
		report.ControlPlane.Provider,
		report.ControlPlane.PolicyMode,
		report.ControlPlane.SupportLevel,
		report.ControlPlane.Selection,
	)
	fmt.Fprintf(&output, "Build: version=%s revision=%s source_state=%s\n", report.Build.Version, report.Build.Revision, report.Build.SourceState)
	fmt.Fprintf(&output, "Snapshot: %d ACL rules, %d nodes, %d proxy hosts, %d evaluated services\n", report.Snapshot.ACLRules, report.Snapshot.Nodes, report.Snapshot.ProxyHosts, report.Snapshot.EvaluatedServices)
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "Services:")
	for _, service := range report.Services {
		fmt.Fprintf(&output, "  %s host_class=%s matches=%v visible_to=%v", service.ID, service.ForwardHostClass, service.StructuralMatchKinds, service.VisibleTo)
		if service.IdentityDependent {
			fmt.Fprint(&output, " identity_dependent=true")
		}
		fmt.Fprintln(&output)
		if service.Private != nil {
			fmt.Fprintf(&output, "    domains=%v forward=%s://%s:%d card=%s\n", service.Private.Domains, service.Private.ForwardScheme, service.Private.ForwardHost, service.Private.ForwardPort, service.Private.CardURL)
		}
	}
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "Identities:")
	for _, identity := range report.Identities {
		serviceIDs := make([]string, len(identity.Services))
		for index, service := range identity.Services {
			serviceIDs[index] = service.ServiceID
		}
		fmt.Fprintf(&output, "  %s services=%v unique=%v\n", identity.Label, serviceIDs, identity.UniqueServices)
		for _, service := range identity.Services {
			if service.Private != nil {
				fmt.Fprintf(&output, "    %s acl=%d source=%s destination=%s kind=%s resolved=%s\n", service.ServiceID, service.ACLIndex, service.Private.SourceToken, service.Private.DestinationSelector, service.DestinationKind, service.Private.ResolvedValue)
			}
		}
	}
	fmt.Fprintf(&output, "Common services: %v\n", report.CommonServices)
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "Findings:")
	if len(report.Findings) == 0 {
		fmt.Fprintln(&output, "  none")
	}
	for _, finding := range report.Findings {
		subject := ""
		if finding.Subject != "" {
			subject = " " + finding.Subject
		}
		fmt.Fprintf(&output, "  %s %s%s: %s\n", strings.ToUpper(finding.Severity), finding.Code, subject, finding.Message)
	}
	fmt.Fprintln(&output)
	fmt.Fprintf(&output, "Scope: %s\n", report.Scope)
	_, err := io.WriteString(writer, output.String())
	return err
}
