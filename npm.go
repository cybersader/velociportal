package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type NPMClient struct {
	baseURL     string
	email       string
	password    string
	httpClient  *http.Client
	token       string
	tokenExpiry time.Time
	mu          sync.Mutex
}

func NewNPMClient(baseURL, email, password string, httpClient *http.Client) *NPMClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &NPMClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		email:      email,
		password:   password,
		httpClient: httpClient,
	}
}

type ProxyHost struct {
	ID            int           `json:"id"`
	DomainNames   []string      `json:"domain_names"`
	ForwardScheme string        `json:"forward_scheme"`
	ForwardHost   string        `json:"forward_host"`
	ForwardPort   int           `json:"forward_port"`
	AccessListID  int           `json:"access_list_id"`
	Enabled       bool          `json:"enabled"`
	Meta          ProxyHostMeta `json:"meta"`
}

type ProxyHostMeta struct {
	NginxOnline bool `json:"nginx_online"`
}

type AccessList struct {
	ID      int                `json:"id"`
	Name    string             `json:"name"`
	Items   []AccessListItem   `json:"items"`
	Clients []AccessListClient `json:"clients"`
}

type AccessListItem struct {
	Username string `json:"username"`
	Hint     string `json:"hint"`
}

type AccessListClient struct {
	Address   string `json:"address"`
	Directive string `json:"directive"`
}

type TokenResponse struct {
	Token   string `json:"token"`
	Expires string `json:"expires"`
}

func (c *NPMClient) authenticate(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{
		"identity": c.email,
		"secret":   c.password,
	})
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authenticate: unexpected status %d: %s", resp.StatusCode, string(b))
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	expiry, err := time.Parse(time.RFC3339, tr.Expires)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}

	c.token = tr.Token
	c.tokenExpiry = expiry
	return nil
}

func (c *NPMClient) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Until(c.tokenExpiry) > time.Hour {
		return nil
	}
	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("ensureToken: %w", err)
	}
	return nil
}

func (c *NPMClient) doRequest(ctx context.Context, method, path string) (*http.Response, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, fmt.Errorf("doRequest: %w", err)
	}

	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		token := c.token
		c.mu.Unlock()
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		return c.httpClient.Do(req)
	}

	resp, err := send()
	if err != nil {
		return nil, fmt.Errorf("doRequest: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		c.mu.Lock()
		reauthErr := c.authenticate(ctx)
		c.mu.Unlock()
		if reauthErr != nil {
			return nil, fmt.Errorf("doRequest: %w", reauthErr)
		}
		resp, err = send()
		if err != nil {
			return nil, fmt.Errorf("doRequest: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("doRequest: unexpected status %d: %s", resp.StatusCode, string(b))
	}

	return resp, nil
}

func (c *NPMClient) FetchProxyHosts(ctx context.Context) ([]ProxyHost, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/nginx/proxy-hosts")
	if err != nil {
		return nil, fmt.Errorf("FetchProxyHosts: %w", err)
	}
	defer resp.Body.Close()

	var hosts []ProxyHost
	if err := json.NewDecoder(resp.Body).Decode(&hosts); err != nil {
		return nil, fmt.Errorf("FetchProxyHosts: %w", err)
	}
	return hosts, nil
}

func (c *NPMClient) FetchAccessLists(ctx context.Context) ([]AccessList, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/nginx/access-lists")
	if err != nil {
		return nil, fmt.Errorf("FetchAccessLists: %w", err)
	}
	defer resp.Body.Close()

	var lists []AccessList
	if err := json.NewDecoder(resp.Body).Decode(&lists); err != nil {
		return nil, fmt.Errorf("FetchAccessLists: %w", err)
	}
	return lists, nil
}
