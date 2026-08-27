package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewUpstreamClientsUseIsolatedHardenedTransports(t *testing.T) {
	cfg := &Config{
		HeadscaleURL:    "https://headscale.example.com",
		HeadscaleAPIKey: "headscale-key",
		NPMURL:          "http://npm.example.com",
		NPMEmail:        "admin@example.com",
		NPMPassword:     "npm-password",
	}
	controlPlane, npm := newUpstreamClients(cfg)
	headscale, ok := controlPlane.(*HeadscaleClient)
	if !ok {
		t.Fatalf("control plane type = %T, want *HeadscaleClient", controlPlane)
	}
	if headscale.httpClient == npm.httpClient {
		t.Fatal("Headscale and NPM share an HTTP client")
	}

	headscaleTransport, ok := headscale.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Headscale transport type = %T", headscale.httpClient.Transport)
	}
	npmTransport, ok := npm.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("NPM transport type = %T", npm.httpClient.Transport)
	}
	if headscaleTransport == npmTransport {
		t.Fatal("Headscale and NPM share an HTTP transport")
	}

	for name, client := range map[string]*http.Client{
		"Headscale": headscale.httpClient,
		"NPM":       npm.httpClient,
	} {
		transport := client.Transport.(*http.Transport)
		if transport.Proxy != nil {
			t.Errorf("%s transport honors environment proxies", name)
		}
		if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
			t.Errorf("%s TLS minimum = %v, want TLS 1.2", name, transport.TLSClientConfig)
		}
		if transport.MaxResponseHeaderBytes != maxUpstreamResponseHeaders {
			t.Errorf("%s max response headers = %d", name, transport.MaxResponseHeaderBytes)
		}
		if client.Timeout != upstreamTimeout {
			t.Errorf("%s timeout = %v, want %v", name, client.Timeout, upstreamTimeout)
		}
		if client.CheckRedirect == nil {
			t.Fatalf("%s redirect policy is nil", name)
		}
		if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
			t.Errorf("%s redirect policy error = %v", name, err)
		}
	}
}

func TestNewUpstreamClientsConstructsSelectedTailscaleProvider(t *testing.T) {
	cfg := &Config{
		ControlPlane:               controlPlaneTailscale,
		TailscaleOAuthClientID:     "client-id",
		TailscaleOAuthClientSecret: "client-secret",
		NPMURL:                     "https://npm.example.com",
		NPMEmail:                   "admin@example.com",
		NPMPassword:                "npm-password",
	}
	controlPlane, npm := newUpstreamClients(cfg)
	tailscale, ok := controlPlane.(*TailscaleClient)
	if !ok {
		t.Fatalf("control plane type = %T, want *TailscaleClient", controlPlane)
	}
	if tailscale.baseURL != tailscaleAPIOrigin {
		t.Fatalf("Tailscale base URL = %q", tailscale.baseURL)
	}
	if tailscale.httpClient == npm.httpClient || tailscale.httpClient.Transport == npm.httpClient.Transport {
		t.Fatal("Tailscale and NPM share an HTTP client or transport")
	}
}

func TestHeadscaleClientRefusesRedirects(t *testing.T) {
	var redirectedHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusFound)
	})
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, r *http.Request) {
		redirectedHits.Add(1)
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := NewHeadscaleClient(server.URL, "headscale-key", nil)
	_, err := client.FetchUsers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Fatalf("FetchUsers() error = %v, want redirect status", err)
	}
	if redirectedHits.Load() != 0 {
		t.Fatal("Headscale client followed a redirect")
	}
}

func TestNPMClientRefusesAuthenticationRedirects(t *testing.T) {
	var redirectedHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusFound)
	})
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, r *http.Request) {
		redirectedHits.Add(1)
		_, _ = w.Write([]byte(`{"token":"redirected"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := NewNPMClient(server.URL, "admin@example.com", "npm-password", nil)
	_, err := client.FetchProxyHosts(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Fatalf("FetchProxyHosts() error = %v, want redirect status", err)
	}
	if redirectedHits.Load() != 0 {
		t.Fatal("NPM client followed an authentication redirect")
	}
}

func TestSuccessfulUpstreamResponsesAreBounded(t *testing.T) {
	oversized := strings.Repeat("x", maxUpstreamResponseBody+1)

	t.Run("Headscale", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(oversized))
		}))
		t.Cleanup(server.Close)

		client := NewHeadscaleClient(server.URL, "headscale-key", server.Client())
		_, err := client.FetchUsers(context.Background())
		if err == nil || !strings.Contains(err.Error(), "response body exceeds") {
			t.Fatalf("FetchUsers() error = %v", err)
		}
	})

	t.Run("NPM", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"token":"jwt-abc","expires":"` + futureExpiry() + `"}`))
		})
		mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(oversized))
		})
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		client := NewNPMClient(server.URL, "admin@example.com", "npm-password", server.Client())
		_, err := client.FetchProxyHosts(context.Background())
		if err == nil || !strings.Contains(err.Error(), "response body exceeds") {
			t.Fatalf("FetchProxyHosts() error = %v", err)
		}
	})
}
