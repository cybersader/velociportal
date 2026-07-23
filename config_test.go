package main

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	setEnv := func(t *testing.T, env map[string]string) {
		t.Helper()
		for k, v := range env {
			t.Setenv(k, v)
		}
	}

	validEnv := map[string]string{
		"HEADSCALE_URL":     "http://headscale:8080",
		"HEADSCALE_API_KEY": "test-key",
		"NPM_URL":           "http://npm:81",
		"NPM_EMAIL":         "admin@example.com",
		"NPM_PASSWORD":      "changeme",
	}

	t.Run("valid config loads", func(t *testing.T) {
		setEnv(t, validEnv)
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.HeadscaleURL != "http://headscale:8080" {
			t.Errorf("HeadscaleURL = %q", cfg.HeadscaleURL)
		}
		if cfg.ListenAddr != "127.0.0.1:8080" {
			t.Errorf("ListenAddr = %q, want 127.0.0.1:8080", cfg.ListenAddr)
		}
		if cfg.PollInterval.String() != "30s" {
			t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
		}
		if cfg.TrustedProxyCIDR.String() != "127.0.0.1/32" {
			t.Errorf("TrustedProxyCIDR = %v, want 127.0.0.1/32", cfg.TrustedProxyCIDR)
		}
	})

	t.Run("missing required env fails", func(t *testing.T) {
		os.Clearenv()
		_, err := loadConfig()
		if err == nil {
			t.Fatal("expected error for missing env")
		}
	})

	t.Run("custom poll interval", func(t *testing.T) {
		setEnv(t, validEnv)
		t.Setenv("POLL_INTERVAL", "1m")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.PollInterval.String() != "1m0s" {
			t.Errorf("PollInterval = %v, want 1m0s", cfg.PollInterval)
		}
	})

	t.Run("invalid poll interval fails", func(t *testing.T) {
		setEnv(t, validEnv)
		t.Setenv("POLL_INTERVAL", "not-a-duration")
		_, err := loadConfig()
		if err == nil {
			t.Fatal("expected error for invalid POLL_INTERVAL")
		}
	})

	t.Run("custom trusted CIDR", func(t *testing.T) {
		setEnv(t, validEnv)
		t.Setenv("TRUSTED_PROXY_CIDR", "100.64.0.0/10")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.TrustedProxyCIDR.String() != "100.64.0.0/10" {
			t.Errorf("TrustedProxyCIDR = %v, want 100.64.0.0/10", cfg.TrustedProxyCIDR)
		}
	})

	t.Run("invalid CIDR fails", func(t *testing.T) {
		setEnv(t, validEnv)
		t.Setenv("TRUSTED_PROXY_CIDR", "not-a-cidr")
		_, err := loadConfig()
		if err == nil {
			t.Fatal("expected error for invalid TRUSTED_PROXY_CIDR")
		}
	})
}
