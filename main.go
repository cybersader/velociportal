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
	"os"
	"os/signal"
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

func loadConfig() (*Config, error) {
	cfg := &Config{
		HeadscaleURL:    os.Getenv("HEADSCALE_URL"),
		HeadscaleAPIKey: os.Getenv("HEADSCALE_API_KEY"),
		NPMURL:          os.Getenv("NPM_URL"),
		NPMEmail:        os.Getenv("NPM_EMAIL"),
		NPMPassword:     os.Getenv("NPM_PASSWORD"),
		ListenAddr:      envOr("LISTEN_ADDR", ":8080"),
	}

	missing := []string{}
	for k, v := range map[string]string{
		"HEADSCALE_URL":     cfg.HeadscaleURL,
		"HEADSCALE_API_KEY": cfg.HeadscaleAPIKey,
		"NPM_URL":           cfg.NPMURL,
		"NPM_EMAIL":         cfg.NPMEmail,
		"NPM_PASSWORD":      cfg.NPMPassword,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("loadConfig: missing required env: %v", missing)
	}

	interval, err := time.ParseDuration(envOr("POLL_INTERVAL", "30s"))
	if err != nil {
		return nil, fmt.Errorf("loadConfig: invalid POLL_INTERVAL: %w", err)
	}
	cfg.PollInterval = interval

	_, cidr, err := net.ParseCIDR(envOr("TRUSTED_PROXY_CIDR", "127.0.0.1/32"))
	if err != nil {
		return nil, fmt.Errorf("loadConfig: invalid TRUSTED_PROXY_CIDR: %w", err)
	}
	cfg.TrustedProxyCIDR = cidr

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

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
		slog.Info("listening", "addr", cfg.ListenAddr, "poll_interval", cfg.PollInterval.String())
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
