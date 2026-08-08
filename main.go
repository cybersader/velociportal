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
	HeadscaleURL     string
	HeadscaleAPIKey  string
	NPMURL           string
	NPMEmail         string
	NPMPassword      string
	ListenAddr       string
	PollInterval     time.Duration
	TrustedProxyCIDR *net.IPNet
}

type configLookup func(string) (string, bool, error)

const (
	minPollInterval       = 5 * time.Second
	maxPollInterval       = 24 * time.Hour
	processEnvEncodingKey = "VELOCIPORTAL_ENV_FILE_ENCODING"
	goQuotedEnvEncoding   = "go-quoted-v1"
)

var requiredConfigKeys = []string{
	"HEADSCALE_URL",
	"HEADSCALE_API_KEY",
	"NPM_URL",
	"NPM_EMAIL",
	"NPM_PASSWORD",
	"TRUSTED_PROXY_CIDR",
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

	values := make(map[string]string, len(requiredConfigKeys))
	missing := make([]string, 0, len(requiredConfigKeys))
	for _, key := range requiredConfigKeys {
		value, _, err := lookup(key)
		if err != nil {
			return nil, fmt.Errorf("loadConfig: %w", err)
		}
		values[key] = value
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("loadConfig: missing required env: %s", strings.Join(missing, ", "))
	}

	headscaleURL, err := normalizeBaseURL("HEADSCALE_URL", values["HEADSCALE_URL"])
	if err != nil {
		return nil, fmt.Errorf("loadConfig: %w", err)
	}
	npmURL, err := normalizeBaseURL("NPM_URL", values["NPM_URL"])
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

	_, trustedProxyCIDR, err := net.ParseCIDR(strings.TrimSpace(values["TRUSTED_PROXY_CIDR"]))
	if err != nil {
		return nil, fmt.Errorf("loadConfig: invalid TRUSTED_PROXY_CIDR: %w", err)
	}
	if trustedProxyCoversAddressSpace(trustedProxyCIDR) {
		return nil, fmt.Errorf("loadConfig: TRUSTED_PROXY_CIDR must not trust an entire IPv4 or IPv6 address space")
	}

	return &Config{
		HeadscaleURL:     headscaleURL,
		HeadscaleAPIKey:  values["HEADSCALE_API_KEY"],
		NPMURL:           npmURL,
		NPMEmail:         strings.TrimSpace(values["NPM_EMAIL"]),
		NPMPassword:      values["NPM_PASSWORD"],
		ListenAddr:       listenAddr,
		PollInterval:     interval,
		TrustedProxyCIDR: trustedProxyCIDR,
	}, nil
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

	httpClient := &http.Client{Timeout: 10 * time.Second}

	hs := NewHeadscaleClient(cfg.HeadscaleURL, cfg.HeadscaleAPIKey, httpClient)
	npm := NewNPMClient(cfg.NPMURL, cfg.NPMEmail, cfg.NPMPassword, httpClient)

	cache := NewCache(hs, npm, cfg.PollInterval, slog.Default())
	cache.Start(ctx)

	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	pollStale := cfg.PollInterval * 3

	mux := http.NewServeMux()
	mux.Handle("GET /", IdentityMiddleware(cfg.TrustedProxyCIDR, NewPortalHandler(cache)))
	mux.Handle("GET /portal", IdentityMiddleware(cfg.TrustedProxyCIDR, NewPortalHandler(cache)))
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
