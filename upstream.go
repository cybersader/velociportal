package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	maxUpstreamResponseHeaders    = 64 * 1024
	maxUpstreamResponseBody       = 8 * 1024 * 1024
	upstreamDialTimeout           = 5 * time.Second
	upstreamTLSHandshakeTimeout   = 5 * time.Second
	upstreamResponseHeaderTimeout = 5 * time.Second
)

func newUpstreamHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{
		Timeout:   upstreamDialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.TLSHandshakeTimeout = upstreamTLSHandshakeTimeout
	transport.ResponseHeaderTimeout = upstreamResponseHeaderTimeout
	transport.MaxResponseHeaderBytes = maxUpstreamResponseHeaders

	return &http.Client{
		Transport: transport,
		Timeout:   upstreamTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newUpstreamClients(cfg *Config) (*HeadscaleClient, *NPMClient) {
	return NewHeadscaleClient(cfg.HeadscaleURL, cfg.HeadscaleAPIKey, newUpstreamHTTPClient()),
		NewNPMClient(cfg.NPMURL, cfg.NPMEmail, cfg.NPMPassword, newUpstreamHTTPClient())
}

func decodeUpstreamJSON(body io.Reader, out any) error {
	contents, err := io.ReadAll(io.LimitReader(body, maxUpstreamResponseBody+1))
	if err != nil {
		return err
	}
	if len(contents) > maxUpstreamResponseBody {
		return fmt.Errorf("response body exceeds %d bytes", maxUpstreamResponseBody)
	}
	if err := json.Unmarshal(contents, out); err != nil {
		return err
	}
	return nil
}
