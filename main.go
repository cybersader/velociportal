package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed assets
var assetsFS embed.FS

type Config struct {
	ControlPlane                controlPlaneProvider
	ControlPlaneExplicit        bool
	InactiveControlPlaneKeys    []string
	ControlPlaneRedactionValues []string
	HeadscaleURL                string
	HeadscaleAPIKey             string
	TailscaleOAuthClientID      string
	TailscaleOAuthClientSecret  string
	NPMURL                      string
	NPMEmail                    string
	NPMPassword                 string
	ServiceMetadataFile         string
	ServiceHealthFile           string
	ListenAddr                  string
	PollInterval                time.Duration
	TrustedProxyCIDR            *net.IPNet
}

type configLookup func(string) (string, bool, error)

const (
	minPollInterval       = 5 * time.Second
	maxPollInterval       = 24 * time.Hour
	processEnvEncodingKey = "VELOCIPORTAL_ENV_FILE_ENCODING"
	goQuotedEnvEncoding   = "go-quoted-v1"

	implicitHeadscaleDeprecationMessage = "CONTROL_PLANE is unset; defaulting to headscale for v0.2 compatibility. Explicit selection becomes required in v0.3."
	implicitHeadscaleDeprecationWarning = "WARNING: " + implicitHeadscaleDeprecationMessage
)

type headscaleTransportClass uint8

const (
	headscaleTransportUnknown headscaleTransportClass = iota
	headscaleTransportVerifiedHTTPS
	headscaleTransportRestrictedHTTP
)

var commonRequiredConfigKeys = []string{
	"NPM_URL",
	"NPM_EMAIL",
	"NPM_PASSWORD",
	"TRUSTED_PROXY_CIDR",
}

var headscaleRequiredConfigKeys = []string{"HEADSCALE_URL", "HEADSCALE_API_KEY"}
var tailscaleRequiredConfigKeys = []string{"TAILSCALE_OAUTH_CLIENT_ID", "TAILSCALE_OAUTH_CLIENT_SECRET"}

// requiredConfigKeys remains the compatibility list used by existing Headscale
// tests and contributor tooling. Provider-aware paths select their own family.
var requiredConfigKeys = append(append([]string(nil), headscaleRequiredConfigKeys...), commonRequiredConfigKeys...)

func controlPlaneConfigKeys(provider controlPlaneProvider) []string {
	if provider == controlPlaneTailscale {
		return tailscaleRequiredConfigKeys
	}
	return headscaleRequiredConfigKeys
}

func inactiveControlPlaneConfigKeys(provider controlPlaneProvider) []string {
	if provider == controlPlaneTailscale {
		return headscaleRequiredConfigKeys
	}
	return tailscaleRequiredConfigKeys
}

func processConfigLookup(key string) (string, bool, error) {
	value, ok := os.LookupEnv(key)
	if !ok || os.Getenv(processEnvEncodingKey) != goQuotedEnvEncoding {
		return value, ok, nil
	}

	decoded, err := parseEnvValue(value)
	if err != nil {
		return "", true, fmt.Errorf("invalid encoded environment value for %s: %w", key, err)
	}
	if err := validateEnvValue(decoded); err != nil {
		return "", true, fmt.Errorf("invalid encoded environment value for %s: %w", key, err)
	}
	return decoded, true, nil
}

func loadConfig() (*Config, error) {
	return loadConfigFrom(processConfigLookup)
}

func loadConfigFrom(lookup configLookup) (*Config, error) {
	if lookup == nil {
		return nil, fmt.Errorf("loadConfig: nil environment lookup")
	}

	selector, selectorSet, err := lookup("CONTROL_PLANE")
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}
	provider := controlPlaneProvider(strings.ToLower(strings.TrimSpace(selector)))
	explicit := selectorSet && provider != ""
	if !explicit {
		provider = controlPlaneHeadscale
	}
	if provider != controlPlaneHeadscale && provider != controlPlaneTailscale {
		return nil, fmt.Errorf("loadConfig: CONTROL_PLANE must be headscale or tailscale")
	}

	providerKeys := controlPlaneConfigKeys(provider)
	required := append(append([]string(nil), providerKeys...), commonRequiredConfigKeys...)
	values := make(map[string]string, len(required)+4)
	missing := make([]string, 0, len(required))
	for _, key := range required {
		value, _, lookupErr := lookup(key)
		if lookupErr != nil {
			return nil, fmt.Errorf("loadConfig: %w", lookupErr)
		}
		values[key] = value
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("loadConfig: missing required env: %s", strings.Join(missing, ", "))
	}

	redactionValues := make([]string, 0, len(providerKeys)+len(inactiveControlPlaneConfigKeys(provider)))
	for _, key := range providerKeys {
		if value := values[key]; value != "" {
			redactionValues = append(redactionValues, value)
		}
	}
	inactiveKeys, inactiveRedactions := inspectInactiveControlPlaneConfig(lookup, provider)
	redactionValues = append(redactionValues, inactiveRedactions...)

	var headscaleURL string
	if provider == controlPlaneHeadscale {
		headscaleURL, err = normalizeHeadscaleBaseURL(values["HEADSCALE_URL"])
		if err != nil {
			return nil, fmt.Errorf("loadConfig: %w", err)
		}
	}
	npmURL, err := normalizeNPMBaseURL(values["NPM_URL"])
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}

	listenValue, err := lookupOr(lookup, "LISTEN_ADDR", "127.0.0.1:8080")
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}
	listenAddr, err := normalizeListenAddr(listenValue)
	if err != nil {
		return nil, fmt.Errorf("loadConfig: invalid LISTEN_ADDR: %w", err)
	}

	pollValue, err := lookupOr(lookup, "POLL_INTERVAL", "30s")
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}
	interval, err := normalizePollInterval(pollValue)
	if err != nil {
		return nil, fmt.Errorf("loadConfig: invalid POLL_INTERVAL: %w", err)
	}

	serviceMetadataFile, err := lookupOr(lookup, "SERVICE_METADATA_FILE", "")
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}
	serviceMetadataFile = strings.TrimSpace(serviceMetadataFile)

	serviceHealthFile, err := lookupOr(lookup, "SERVICE_HEALTH_FILE", "")
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}
	serviceHealthFile = strings.TrimSpace(serviceHealthFile)

	_, trustedProxyCIDR, err := net.ParseCIDR(strings.TrimSpace(values["TRUSTED_PROXY_CIDR"]))
	if err != nil {
		return nil, fmt.Errorf("loadConfig: invalid TRUSTED_PROXY_CIDR: %w", err)
	}
	if trustedProxyCoversAddressSpace(trustedProxyCIDR) {
		return nil, fmt.Errorf("loadConfig: TRUSTED_PROXY_CIDR must not trust an entire IPv4 or IPv6 address space")
	}

	return &Config{
		ControlPlane:                provider,
		ControlPlaneExplicit:        explicit,
		InactiveControlPlaneKeys:    inactiveKeys,
		ControlPlaneRedactionValues: redactionValues,
		HeadscaleURL:                headscaleURL,
		HeadscaleAPIKey:             values["HEADSCALE_API_KEY"],
		TailscaleOAuthClientID:      strings.TrimSpace(values["TAILSCALE_OAUTH_CLIENT_ID"]),
		TailscaleOAuthClientSecret:  values["TAILSCALE_OAUTH_CLIENT_SECRET"],
		NPMURL:                      npmURL,
		NPMEmail:                    strings.TrimSpace(values["NPM_EMAIL"]),
		NPMPassword:                 values["NPM_PASSWORD"],
		ServiceMetadataFile:         serviceMetadataFile,
		ServiceHealthFile:           serviceHealthFile,
		ListenAddr:                  listenAddr,
		PollInterval:                interval,
		TrustedProxyCIDR:            trustedProxyCIDR,
	}, nil
}

func inspectInactiveControlPlaneConfig(lookup configLookup, provider controlPlaneProvider) ([]string, []string) {
	keys := make([]string, 0, len(inactiveControlPlaneConfigKeys(provider)))
	redactions := make([]string, 0, len(inactiveControlPlaneConfigKeys(provider)))
	for _, key := range inactiveControlPlaneConfigKeys(provider) {
		value, ok, err := lookup(key)
		if err != nil {
			if ok {
				keys = append(keys, key)
			}
			continue
		}
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
		redactions = append(redactions, value)
	}
	return keys, redactions
}

func lookupOr(lookup configLookup, key, fallback string) (string, error) {
	value, ok, err := lookup(key)
	if err != nil {
		return "", err
	}
	if ok && strings.TrimSpace(value) != "" {
		return value, nil
	}
	return fallback, nil
}

func normalizePollInterval(raw string) (time.Duration, error) {
	interval, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	if interval < minPollInterval || interval > maxPollInterval {
		return 0, fmt.Errorf("must be between %s and %s", minPollInterval, maxPollInterval)
	}
	return interval, nil
}

func trustedProxyCoversAddressSpace(network *net.IPNet) bool {
	if network == nil {
		return true
	}
	allIPv4 := network.Contains(net.IPv4zero) && network.Contains(net.IPv4bcast)
	ipv6Last := net.ParseIP("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
	allIPv6 := network.Contains(net.IPv6zero) && network.Contains(ipv6Last)
	return allIPv4 || allIPv6
}

func normalizeHeadscaleBaseURL(raw string) (string, error) {
	normalized, err := normalizeBaseURL("HEADSCALE_URL", raw)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid HEADSCALE_URL: %w", err)
	}
	switch classifyHeadscaleTransport(normalized) {
	case headscaleTransportVerifiedHTTPS:
		return normalized, nil
	case headscaleTransportRestrictedHTTP:
		if allowedHeadscaleHTTPHost(parsed.Hostname()) {
			return normalized, nil
		}
		return "", fmt.Errorf("invalid HEADSCALE_URL: http is allowed only for the canonical internal or same-host route")
	default:
		return "", fmt.Errorf("invalid HEADSCALE_URL: scheme must be http or https")
	}
}

func allowedHeadscaleHTTPHost(host string) bool {
	return allowedRestrictedHTTPHost(host, "headscale.velociportal.internal")
}

func normalizeNPMBaseURL(raw string) (string, error) {
	normalized, err := normalizeBaseURL("NPM_URL", raw)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid NPM_URL: %w", err)
	}
	if parsed.Scheme == "https" {
		return normalized, nil
	}
	if parsed.Scheme == "http" && allowedRestrictedHTTPHost(parsed.Hostname(), "npm.velociportal.internal") {
		return normalized, nil
	}
	return "", fmt.Errorf("invalid NPM_URL: http is allowed only for the canonical internal or same-host route")
}

func allowedRestrictedHTTPHost(host, canonical string) bool {
	if strings.EqualFold(host, canonical) ||
		strings.EqualFold(host, "host.docker.internal") ||
		strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if strings.Contains(host, ":") {
		return ip.Equal(net.IPv6loopback)
	}
	ipv4 := ip.To4()
	return ipv4 != nil && ipv4[0] == 127
}

func classifyHeadscaleTransport(raw string) headscaleTransportClass {
	parsed, err := url.Parse(raw)
	if err != nil {
		return headscaleTransportUnknown
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return headscaleTransportVerifiedHTTPS
	case "http":
		return headscaleTransportRestrictedHTTP
	default:
		return headscaleTransportUnknown
	}
}

func normalizeBaseURL(name, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid %s: %w", name, err)
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid %s: scheme must be http or https", name)
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid %s: absolute URL with a host is required", name)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("invalid %s: user information is not allowed", name)
	}
	if parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("invalid %s: query and fragment are not allowed", name)
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("invalid %s: port must not be empty", name)
	}
	if portText := parsed.Port(); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid %s: port must be a number from 1 to 65535", name)
		}
	}

	if parsed.RawPath != "" {
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
		parsed.Path, err = url.PathUnescape(parsed.RawPath)
		if err != nil {
			return "", fmt.Errorf("invalid %s path: %w", name, err)
		}
	} else {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	return parsed.String(), nil
}

func normalizeListenAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("must be an explicit host:port: %w", err)
	}
	if host == "" {
		return "", fmt.Errorf("host must not be empty")
	}

	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("port must be a number from 1 to 65535")
	}

	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("localhost", strconv.Itoa(port)), nil
	}

	ipHost := host
	zone := ""
	if percent := strings.LastIndexByte(ipHost, '%'); percent >= 0 {
		if percent == 0 || percent == len(ipHost)-1 || strings.Contains(ipHost[:percent], "%") {
			return "", fmt.Errorf("IPv6 zone must be non-empty and appear only once")
		}
		zone = ipHost[percent:]
		ipHost = ipHost[:percent]
	}
	ip := net.ParseIP(ipHost)
	if ip == nil {
		return "", fmt.Errorf("host must be localhost or an IP address")
	}
	if zone != "" && ip.To4() != nil {
		return "", fmt.Errorf("zone identifiers are only valid for IPv6 addresses")
	}
	if ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return "", fmt.Errorf("host must not be multicast or broadcast")
	}

	return net.JoinHostPort(ip.String()+zone, strconv.Itoa(port)), nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run() error {
	return runWithLookup(processConfigLookup)
}

func runWithLookup(lookup configLookup) error {
	cfg, err := loadConfigFrom(lookup)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return runServer(cfg)
}

func runServer(cfg *Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !cfg.ControlPlaneExplicit {
		slog.Warn(implicitHeadscaleDeprecationMessage)
	}
	if len(cfg.InactiveControlPlaneKeys) > 0 {
		slog.Warn("inactive control-plane configuration is ignored", "keys", strings.Join(cfg.InactiveControlPlaneKeys, ","))
	}
	controlPlane, npm := newUpstreamClients(cfg)

	cache := NewCacheWithServiceMetadata(
		controlPlane,
		npm,
		serviceMetadataLoaderForPath(cfg.ServiceMetadataFile),
		cfg.PollInterval,
		slog.Default(),
	)
	cache.Start(ctx)

	protectedHealthURLs := serviceHealthProtectedURLs(cfg)
	healthPoller := NewServiceHealthPoller(
		cache,
		serviceHealthConfigLoaderForPath(cfg.ServiceHealthFile),
		func(config *ServiceHealthConfig) serviceHealthProber {
			engine, engineErr := newServiceProbeEngine(config, protectedHealthURLs)
			if engineErr != nil {
				slog.Error("service health probe engine initialization failed")
				return nil
			}
			return engine
		},
		slog.Default(),
	)
	healthPoller.Start(ctx)

	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	pollStale := cfg.PollInterval * 3

	mux := http.NewServeMux()
	portalHandler := IdentityMiddleware(cfg.TrustedProxyCIDR, NewPortalHandlerWithHealth(cache, healthPoller.Store()))
	mux.Handle("GET /", portalHandler)
	mux.Handle("GET /portal", portalHandler)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		age := time.Since(cache.LastUpdated())
		if cache.LastUpdated().IsZero() || age > pollStale {
			http.Error(w, fmt.Sprintf("stale cache: age=%s", age.Round(time.Second)), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "ok cache_age=%s\n", age.Round(time.Second))
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr, "poll_interval", cfg.PollInterval.String(), "trusted_proxy_cidr", cfg.TrustedProxyCIDR.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("run: %w", err)
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("run: shutdown: %w", err)
	}
	slog.Info("shutdown complete")
	return nil
}
