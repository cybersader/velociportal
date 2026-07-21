package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type HeadscaleClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewHeadscaleClient(baseURL, apiKey string, httpClient *http.Client) *HeadscaleClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &HeadscaleClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

type Policy struct {
	Groups    map[string][]string `json:"groups"`
	TagOwners map[string][]string `json:"tagOwners"`
	ACLs      []ACLRule           `json:"acls"`
	Hosts     map[string]string   `json:"hosts"`
}

type ACLRule struct {
	Action string   `json:"action"`
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type Node struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	User        User     `json:"user"`
	ForcedTags  []string `json:"forcedTags"`
	IPAddresses []string `json:"ipAddresses"`
}

func (c *HeadscaleClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get: %s returned status %d", path, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("get: decode %s: %w", path, err)
	}
	return nil
}

func (c *HeadscaleClient) FetchPolicy(ctx context.Context) (*Policy, error) {
	var p Policy
	if err := c.get(ctx, "/api/v1/policy", &p); err != nil {
		return nil, fmt.Errorf("FetchPolicy: %w", err)
	}
	return &p, nil
}

func (c *HeadscaleClient) FetchUsers(ctx context.Context) ([]User, error) {
	var users []User
	if err := c.get(ctx, "/api/v1/user", &users); err != nil {
		return nil, fmt.Errorf("FetchUsers: %w", err)
	}
	return users, nil
}

func (c *HeadscaleClient) FetchNodes(ctx context.Context) ([]Node, error) {
	var nodes []Node
	if err := c.get(ctx, "/api/v1/node", &nodes); err != nil {
		return nil, fmt.Errorf("FetchNodes: %w", err)
	}
	return nodes, nil
}
