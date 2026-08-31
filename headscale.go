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

const maxHeadscaleErrorBody = 4096

type HeadscaleClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewHeadscaleClient(baseURL, apiKey string, httpClient *http.Client) *HeadscaleClient {
	if httpClient == nil {
		httpClient = newUpstreamHTTPClient()
	}
	return &HeadscaleClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

func (c *HeadscaleClient) Provider() controlPlaneProvider { return controlPlaneHeadscale }

type headscaleUserDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

type headscaleNodeDTO struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	User        headscaleUserDTO `json:"user"`
	Tags        []string         `json:"tags"`
	ForcedTags  []string         `json:"forcedTags"`
	ValidTags   []string         `json:"validTags"`
	IPAddresses []string         `json:"ipAddresses"`
}

// trailingCommaRE matches a comma that is followed only by whitespace and a
// closing brace or bracket — an artifact of huJSON that standard encoding/json
// rejects.
var trailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

// standardizeHuJSON converts the limited huJSON form returned by Headscale into
// strict JSON. This deliberately preserves the implementation's existing
// comment and trailing-comma behavior.
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

func headscaleErrorExcerpt(body io.Reader, apiKey string) string {
	if body == nil {
		return "response body unavailable"
	}
	contents, err := io.ReadAll(io.LimitReader(body, maxHeadscaleErrorBody+1))
	if err != nil {
		return "response body unreadable"
	}
	if len(contents) > maxHeadscaleErrorBody {
		contents = contents[:maxHeadscaleErrorBody]
	}
	if len(contents) == 0 {
		return "response body empty"
	}
	return sanitizeDoctorError(fmt.Errorf("%s", contents), []string{apiKey})
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
		excerpt := headscaleErrorExcerpt(resp.Body, c.apiKey)
		return fmt.Errorf("get: %s returned status %d: %s", path, resp.StatusCode, excerpt)
	}

	if err := decodeUpstreamJSON(resp.Body, out); err != nil {
		return fmt.Errorf("get: decode %s: %w", path, err)
	}
	return nil
}

func (c *HeadscaleClient) FetchPolicy(ctx context.Context) (*Policy, error) {
	validated, err := c.fetchPolicy(ctx)
	if err != nil {
		return nil, err
	}
	return validated.Policy, nil
}

func (c *HeadscaleClient) fetchPolicy(ctx context.Context) (*validatedPolicy, error) {
	start := time.Now()
	var wrapper struct {
		Policy    *string `json:"policy"`
		UpdatedAt string  `json:"updatedAt"`
	}
	if err := c.get(ctx, "/api/v1/policy", &wrapper); err != nil {
		return nil, fmt.Errorf("FetchPolicy: %w", err)
	}
	if wrapper.Policy == nil {
		return nil, fmt.Errorf("FetchPolicy: response is missing policy")
	}

	if strings.TrimSpace(*wrapper.Policy) == "" {
		slog.Info("headscale: fetched policy (empty)",
			"path", "/api/v1/policy", "duration", time.Since(start))
		return &validatedPolicy{Policy: &Policy{}, PolicyMode: legacyACLVisibilityV1}, nil
	}

	raw := []byte(*wrapper.Policy)
	validated, err := validateLegacyPolicyDocument(raw)
	if err != nil {
		standardized := standardizeHuJSON(raw)
		if json.Valid(standardized) {
			validated, err = validateLegacyPolicyDocument(standardized)
		}
		if err != nil {
			return nil, fmt.Errorf("FetchPolicy: parse policy document: %w", err)
		}
	}

	slog.Info("headscale: fetched policy",
		"path", "/api/v1/policy",
		"duration", time.Since(start),
		"groups", len(validated.Policy.Groups),
		"tagOwners", len(validated.Policy.TagOwners),
		"access_rules", validated.Policy.accessRuleCount())
	return validated, nil
}

// FetchUsers is retained for Headscale API compatibility tests. Runtime loading
// intentionally does not call the user endpoint because node DTOs already carry
// the owner identity needed by the matcher.
func (c *HeadscaleClient) FetchUsers(ctx context.Context) ([]headscaleUserDTO, error) {
	start := time.Now()
	var wrapper struct {
		Users *[]headscaleUserDTO `json:"users"`
	}
	if err := c.get(ctx, "/api/v1/user", &wrapper); err != nil {
		return nil, fmt.Errorf("FetchUsers: %w", err)
	}
	if wrapper.Users == nil {
		return nil, fmt.Errorf("FetchUsers: response is missing users")
	}
	slog.Info("headscale: fetched users",
		"path", "/api/v1/user",
		"duration", time.Since(start),
		"count", len(*wrapper.Users))
	return *wrapper.Users, nil
}

type headscaleNodeLoad struct {
	Nodes          []Node
	CandidateNames []string
}

func (c *HeadscaleClient) FetchNodes(ctx context.Context) ([]Node, error) {
	loaded, err := c.fetchNodes(ctx, false)
	return loaded.Nodes, err
}

func (c *HeadscaleClient) fetchNodes(ctx context.Context, includeCandidateNames bool) (headscaleNodeLoad, error) {
	start := time.Now()
	var wrapper struct {
		Nodes *[]headscaleNodeDTO `json:"nodes"`
	}
	if err := c.get(ctx, "/api/v1/node", &wrapper); err != nil {
		return headscaleNodeLoad{}, fmt.Errorf("FetchNodes: %w", err)
	}
	if wrapper.Nodes == nil {
		return headscaleNodeLoad{}, fmt.Errorf("FetchNodes: response is missing nodes")
	}

	loaded := headscaleNodeLoad{Nodes: make([]Node, 0, len(*wrapper.Nodes))}
	if includeCandidateNames {
		loaded.CandidateNames = make([]string, 0, len(*wrapper.Nodes))
	}
	for _, dto := range *wrapper.Nodes {
		tags := make([]string, 0, len(dto.Tags)+len(dto.ForcedTags)+len(dto.ValidTags))
		tags = append(tags, dto.Tags...)
		tags = append(tags, dto.ForcedTags...)
		tags = append(tags, dto.ValidTags...)
		loaded.Nodes = append(loaded.Nodes, Node{
			ID:         strings.TrimSpace(dto.ID),
			Name:       strings.TrimSpace(dto.Name),
			OwnerLogin: strings.TrimSpace(dto.User.Name),
			Tags:       normalizeStrings(tags),
			Addresses:  normalizeStrings(dto.IPAddresses),
		})
		if includeCandidateNames {
			loaded.CandidateNames = append(loaded.CandidateNames, dto.Name)
		}
	}

	slog.Info("headscale: fetched nodes",
		"path", "/api/v1/node",
		"duration", time.Since(start),
		"count", len(loaded.Nodes))
	return loaded, nil
}

func (c *HeadscaleClient) Load(ctx context.Context, progress controlPlaneProgress) (*ControlPlaneResult, error) {
	result, _, err := c.load(ctx, progress, false)
	return result, err
}

func (c *HeadscaleClient) LoadHostnameSuggestions(ctx context.Context, progress controlPlaneProgress) (*ControlPlaneResult, []string, error) {
	return c.load(ctx, progress, true)
}

func (c *HeadscaleClient) load(ctx context.Context, progress controlPlaneProgress, includeCandidateNames bool) (*ControlPlaneResult, []string, error) {
	policyResult, err := call(ctx, c.fetchPolicy)
	if err != nil {
		return nil, nil, &controlPlaneLoadError{Provider: c.Provider(), Stage: controlPlaneStagePolicy, Err: err}
	}
	reportControlPlaneProgress(progress, controlPlaneStagePolicy, policyResult.Policy.accessRuleCount())

	nodeLoad, err := call(ctx, func(ctx context.Context) (headscaleNodeLoad, error) {
		return c.fetchNodes(ctx, includeCandidateNames)
	})
	if err != nil {
		return nil, nil, &controlPlaneLoadError{Provider: c.Provider(), Stage: controlPlaneStageNodes, Err: err}
	}
	reportControlPlaneProgress(progress, controlPlaneStageNodes, len(nodeLoad.Nodes))

	return &ControlPlaneResult{
		Policy: policyResult.Policy,
		Nodes:  nodeLoad.Nodes,
		Metadata: ControlPlaneMetadata{
			Provider:     c.Provider(),
			PolicyMode:   policyResult.PolicyMode,
			SupportLevel: controlPlaneSupported,
			SSHPresent:   policyResult.SSHPresent,
		},
	}, nodeLoad.CandidateNames, nil
}
