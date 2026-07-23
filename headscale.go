package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
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
	ValidTags   []string `json:"validTags"`
	IPAddresses []string `json:"ipAddresses"`
}

// trailingCommaRE matches a comma that is followed only by whitespace and a
// closing brace or bracket — an artifact of huJSON that standard encoding/json
// rejects.
var trailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

// standardizeHuJSON converts a huJSON (Human JSON) document into strict JSON
// that encoding/json can parse. It strips `//` line comments and removes
// trailing commas before `}` and `]`. It deliberately does NOT attempt to
// handle `//` sequences inside string literals — Headscale policy documents
// never contain those, so the simple line-based approach is safe here.
func standardizeHuJSON(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	joined := strings.Join(lines, "\n")
	return trailingCommaRE.ReplaceAll([]byte(joined), []byte("$1"))
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("get: %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("get: decode %s: %w", path, err)
	}
	return nil
}

func (c *HeadscaleClient) FetchPolicy(ctx context.Context) (*Policy, error) {
	start := time.Now()

	// The policy endpoint wraps the policy document as a JSON-encoded string,
	// which may itself contain huJSON features (comments, trailing commas).
	var wrapper struct {
		Policy    string `json:"policy"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := c.get(ctx, "/api/v1/policy", &wrapper); err != nil {
		return nil, fmt.Errorf("FetchPolicy: %w", err)
	}

	var p Policy
	if strings.TrimSpace(wrapper.Policy) == "" {
		// No policy defined (Headscale default is allow-all). Return an empty
		// policy rather than failing to parse an empty string.
		slog.Info("headscale: fetched policy (empty)",
			"path", "/api/v1/policy", "duration", time.Since(start))
		return &p, nil
	}

	raw := []byte(wrapper.Policy)
	if err := json.Unmarshal(raw, &p); err != nil {
		// Fall back to standardizing huJSON before giving up.
		if err2 := json.Unmarshal(standardizeHuJSON(raw), &p); err2 != nil {
			return nil, fmt.Errorf("FetchPolicy: parse policy document: %w", err2)
		}
	}

	slog.Info("headscale: fetched policy",
		"path", "/api/v1/policy",
		"duration", time.Since(start),
		"groups", len(p.Groups),
		"tagOwners", len(p.TagOwners),
		"acls", len(p.ACLs))
	return &p, nil
}

func (c *HeadscaleClient) FetchUsers(ctx context.Context) ([]User, error) {
	start := time.Now()
	var wrapper struct {
		Users []User `json:"users"`
	}
	if err := c.get(ctx, "/api/v1/user", &wrapper); err != nil {
		return nil, fmt.Errorf("FetchUsers: %w", err)
	}
	slog.Info("headscale: fetched users",
		"path", "/api/v1/user",
		"duration", time.Since(start),
		"count", len(wrapper.Users))
	return wrapper.Users, nil
}

func (c *HeadscaleClient) FetchNodes(ctx context.Context) ([]Node, error) {
	start := time.Now()
	var wrapper struct {
		Nodes []Node `json:"nodes"`
	}
	if err := c.get(ctx, "/api/v1/node", &wrapper); err != nil {
		return nil, fmt.Errorf("FetchNodes: %w", err)
	}
	slog.Info("headscale: fetched nodes",
		"path", "/api/v1/node",
		"duration", time.Since(start),
		"count", len(wrapper.Nodes))
	return wrapper.Nodes, nil
}
