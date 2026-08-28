package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tailscaleAPIOrigin  = "https://api.tailscale.com/api/v2"
	tailscaleTokenSkew  = 5 * time.Minute
	maxTailscaleErrBody = 4096
)

var tailscaleReadScopes = []string{
	"policy_file:read",
	"devices:posture_attributes:read",
	"devices:core:read",
	"users:read",
}

type TailscaleClient struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
	now          func() time.Time

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

func NewTailscaleClient(clientID, clientSecret string, httpClient *http.Client) *TailscaleClient {
	return newTailscaleClient(tailscaleAPIOrigin, clientID, clientSecret, httpClient, time.Now)
}

func newTailscaleClientForTest(baseURL, clientID, clientSecret string, httpClient *http.Client, now func() time.Time) *TailscaleClient {
	return newTailscaleClient(baseURL, clientID, clientSecret, httpClient, now)
}

func newTailscaleClient(baseURL, clientID, clientSecret string, httpClient *http.Client, now func() time.Time) *TailscaleClient {
	if httpClient == nil {
		httpClient = newUpstreamHTTPClient()
	}
	if now == nil {
		now = time.Now
	}
	return &TailscaleClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   httpClient,
		now:          now,
	}
}

func (c *TailscaleClient) Provider() controlPlaneProvider { return controlPlaneTailscale }

type tailscaleTokenDTO struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

type tailscaleUserDTO struct {
	ID        json.RawMessage `json:"id"`
	LoginName string          `json:"loginName"`
}

type tailscaleDeviceDTO struct {
	ID        json.RawMessage `json:"id"`
	NodeID    string          `json:"nodeId"`
	Name      string          `json:"name"`
	Hostname  string          `json:"hostname"`
	User      json.RawMessage `json:"user"`
	Addresses []string        `json:"addresses"`
	Tags      []string        `json:"tags"`
}

func (c *TailscaleClient) Load(ctx context.Context, progress controlPlaneProgress) (_ *ControlPlaneResult, err error) {
	defer func() {
		if err == nil {
			return
		}
		var loadErr *controlPlaneLoadError
		if errors.As(err, &loadErr) {
			err = &controlPlaneLoadError{
				Provider: loadErr.Provider,
				Stage:    loadErr.Stage,
				Err:      c.redactError(loadErr.Err),
			}
			return
		}
		err = c.redactError(err)
	}()

	if _, err := c.token(ctx, ""); err != nil {
		return nil, &controlPlaneLoadError{Provider: c.Provider(), Stage: controlPlaneStageAuth, Err: err}
	}
	reportControlPlaneProgress(progress, controlPlaneStageAuth, 0)

	policyResult, err := c.fetchPolicy(ctx)
	if err != nil {
		return nil, &controlPlaneLoadError{Provider: c.Provider(), Stage: controlPlaneStagePolicy, Err: err}
	}
	reportControlPlaneProgress(progress, controlPlaneStagePolicy, policyResult.Policy.accessRuleCount())

	users, err := c.fetchUsers(ctx)
	if err != nil {
		return nil, &controlPlaneLoadError{Provider: c.Provider(), Stage: controlPlaneStageUsers, Err: err}
	}
	reportControlPlaneProgress(progress, controlPlaneStageUsers, len(users))

	nodes, err := c.fetchDevices(ctx, users)
	if err != nil {
		return nil, &controlPlaneLoadError{Provider: c.Provider(), Stage: controlPlaneStageDevices, Err: err}
	}
	reportControlPlaneProgress(progress, controlPlaneStageDevices, len(nodes))

	return &ControlPlaneResult{
		Policy: policyResult.Policy,
		Nodes:  nodes,
		Metadata: ControlPlaneMetadata{
			Provider:     c.Provider(),
			PolicyMode:   policyResult.PolicyMode,
			SupportLevel: controlPlanePreview,
			SSHPresent:   policyResult.SSHPresent,
		},
	}, nil
}

func (c *TailscaleClient) fetchPolicy(ctx context.Context) (*validatedPolicy, error) {
	body, _, err := c.doAPI(ctx, http.MethodGet, "/tailnet/-/acl")
	if err != nil {
		return nil, fmt.Errorf("fetch policy: %w", err)
	}
	validated, err := validatePolicyDocument(body)
	if err != nil {
		return nil, fmt.Errorf("fetch policy: %w", err)
	}
	return validated, nil
}

func (c *TailscaleClient) fetchUsers(ctx context.Context) ([]tailscaleUserDTO, error) {
	body, header, err := c.doAPI(ctx, http.MethodGet, "/tailnet/-/users")
	if err != nil {
		return nil, fmt.Errorf("fetch users: %w", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("fetch users: decode response: %w", err)
	}
	if err := rejectPartialTailscaleResponse(envelope, header); err != nil {
		return nil, fmt.Errorf("fetch users: %w", err)
	}
	rawUsers, ok := envelope["users"]
	if !ok {
		return nil, fmt.Errorf("fetch users: response is missing users")
	}
	if bytes.Equal(bytes.TrimSpace(rawUsers), []byte("null")) {
		return nil, fmt.Errorf("fetch users: users must be an array")
	}
	var users []tailscaleUserDTO
	if err := json.Unmarshal(rawUsers, &users); err != nil {
		return nil, fmt.Errorf("fetch users: decode users: %w", err)
	}
	if err := validateTailscaleUsers(users); err != nil {
		return nil, fmt.Errorf("fetch users: %w", err)
	}
	return users, nil
}

func validateTailscaleUsers(users []tailscaleUserDTO) error {
	ids := make(map[string]int, len(users))
	logins := make(map[string]int, len(users))
	for index, user := range users {
		id, err := tailscaleReference(user.ID)
		if err != nil || id == "" {
			return fmt.Errorf("user %d has a blank or invalid id", index)
		}
		login := strings.TrimSpace(user.LoginName)
		if login == "" {
			return fmt.Errorf("user %d has a blank loginName", index)
		}
		if previous, exists := ids[id]; exists {
			return fmt.Errorf("users %d and %d have duplicate id", previous, index)
		}
		if previous, exists := logins[login]; exists {
			return fmt.Errorf("users %d and %d have duplicate loginName", previous, index)
		}
		ids[id] = index
		logins[login] = index
	}
	for id, idIndex := range ids {
		if loginIndex, exists := logins[id]; exists && loginIndex != idIndex {
			return fmt.Errorf("users %d and %d create an ambiguous owner reference", idIndex, loginIndex)
		}
	}
	return nil
}

func (c *TailscaleClient) fetchDevices(ctx context.Context, users []tailscaleUserDTO) ([]Node, error) {
	body, header, err := c.doAPI(ctx, http.MethodGet, "/tailnet/-/devices")
	if err != nil {
		return nil, fmt.Errorf("fetch devices: %w", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("fetch devices: decode response: %w", err)
	}
	if err := rejectPartialTailscaleResponse(envelope, header); err != nil {
		return nil, fmt.Errorf("fetch devices: %w", err)
	}
	rawDevices, ok := envelope["devices"]
	if !ok {
		return nil, fmt.Errorf("fetch devices: response is missing devices")
	}
	if bytes.Equal(bytes.TrimSpace(rawDevices), []byte("null")) {
		return nil, fmt.Errorf("fetch devices: devices must be an array")
	}
	var devices []tailscaleDeviceDTO
	if err := json.Unmarshal(rawDevices, &devices); err != nil {
		return nil, fmt.Errorf("fetch devices: decode devices: %w", err)
	}

	nodes := make([]Node, 0, len(devices))
	seenIDs := make(map[string]int, len(devices))
	for index, device := range devices {
		id, err := tailscaleReference(device.ID)
		if err != nil {
			return nil, fmt.Errorf("fetch devices: device %d has invalid id", index)
		}
		if id == "" {
			id = strings.TrimSpace(device.NodeID)
		}
		if id == "" {
			return nil, fmt.Errorf("fetch devices: device %d has a blank id", index)
		}
		if previous, exists := seenIDs[id]; exists {
			return nil, fmt.Errorf("fetch devices: devices %d and %d have duplicate id", previous, index)
		}
		seenIDs[id] = index

		tags := normalizeStrings(device.Tags)
		ownerRef, err := tailscaleReference(device.User)
		if err != nil {
			return nil, fmt.Errorf("fetch devices: device %d has an invalid owner reference", index)
		}
		owner := ""
		if len(tags) == 0 {
			if ownerRef == "" {
				return nil, fmt.Errorf("fetch devices: untagged device %d has a blank owner reference", index)
			}
			owner, err = resolveTailscaleOwner(ownerRef, users)
			if err != nil {
				return nil, fmt.Errorf("fetch devices: device %d: %w", index, err)
			}
		}
		name := strings.TrimSpace(device.Name)
		if name == "" {
			name = strings.TrimSpace(device.Hostname)
		}
		nodes = append(nodes, Node{
			ID:         id,
			Name:       name,
			OwnerLogin: owner,
			Tags:       tags,
			Addresses:  normalizeStrings(device.Addresses),
		})
	}
	return nodes, nil
}

func resolveTailscaleOwner(reference string, users []tailscaleUserDTO) (string, error) {
	matches := make(map[string]struct{})
	for _, user := range users {
		id, _ := tailscaleReference(user.ID)
		login := strings.TrimSpace(user.LoginName)
		if reference == id || reference == login {
			matches[login] = struct{}{}
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("owner reference does not resolve to a user")
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("owner reference is ambiguous")
	}
	for login := range matches {
		return login, nil
	}
	panic("unreachable")
}

func tailscaleReference(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("reference must be a string or number")
}

func rejectPartialTailscaleResponse(envelope map[string]json.RawMessage, header http.Header) error {
	for key := range envelope {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
		switch normalized {
		case "next", "nextpage", "cursor", "continuation", "hasmore", "partial", "truncated":
			return fmt.Errorf("paginated or partial response is not supported")
		}
	}
	for _, key := range []string{"Content-Range", "Link", "X-Next-Page", "X-Next-Cursor", "X-Continuation-Token"} {
		if strings.TrimSpace(header.Get(key)) != "" {
			return fmt.Errorf("paginated or partial response is not supported")
		}
	}
	return nil
}

func (c *TailscaleClient) doAPI(ctx context.Context, method, path string) ([]byte, http.Header, error) {
	token, err := c.token(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	body, header, status, err := c.send(ctx, method, path, token)
	if err != nil {
		return nil, nil, c.redactErrorWith(err, token)
	}
	var rejectedToken string
	if status == http.StatusUnauthorized {
		rejectedToken = token
		token, err = c.token(ctx, rejectedToken)
		if err != nil {
			return nil, nil, c.redactErrorWith(err, rejectedToken)
		}
		body, header, status, err = c.send(ctx, method, path, token)
		if err != nil {
			return nil, nil, c.redactErrorWith(err, rejectedToken, token)
		}
	}
	if status != http.StatusOK {
		return nil, nil, c.redactErrorWith(fmt.Errorf("%s returned status %d: %s", path, status, tailscaleErrorExcerpt(body)), rejectedToken, token)
	}
	return body, header, nil
}

func (c *TailscaleClient) send(ctx context.Context, method, path, token string) ([]byte, http.Header, int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := readBoundedUpstreamBody(resp.Body)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read response: %w", err)
	}
	return body, resp.Header.Clone(), resp.StatusCode, nil
}

func (c *TailscaleClient) token(ctx context.Context, rejected string) (string, error) {
	c.mu.Lock()
	if c.accessToken != "" && c.accessToken != rejected && c.now().Add(tailscaleTokenSkew).Before(c.tokenExpiry) {
		token := c.accessToken
		c.mu.Unlock()
		return token, nil
	}
	err := c.authenticateLocked(ctx)
	token := c.accessToken
	c.mu.Unlock()
	if err != nil {
		return "", c.redactError(fmt.Errorf("oauth token: %w", err))
	}
	return token, nil
}

func (c *TailscaleClient) authenticateLocked(ctx context.Context) error {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("scope", strings.Join(tailscaleReadScopes, " "))

	requestCtx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.baseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := readBoundedUpstreamBody(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("returned status %d: %s", resp.StatusCode, tailscaleErrorExcerpt(body))
	}
	var token tailscaleTokenDTO
	if err := json.Unmarshal(body, &token); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return fmt.Errorf("response access_token is blank")
	}
	if token.ExpiresIn <= 0 {
		return fmt.Errorf("response expires_in must be positive")
	}
	if token.TokenType != "" && !strings.EqualFold(token.TokenType, "bearer") {
		return fmt.Errorf("response token_type %q is not bearer", token.TokenType)
	}
	if strings.TrimSpace(token.Scope) != "" && !sameStringSet(strings.Fields(token.Scope), tailscaleReadScopes) {
		return fmt.Errorf("response scope does not match the requested read scopes")
	}
	c.accessToken = token.AccessToken
	c.tokenExpiry = c.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		if counts[value] == 0 {
			return false
		}
		counts[value]--
	}
	return true
}

func tailscaleErrorExcerpt(body []byte) string {
	if len(body) == 0 {
		return "response body empty"
	}
	if len(body) > maxTailscaleErrBody {
		body = body[:maxTailscaleErrBody]
	}
	return strings.TrimSpace(strconv.QuoteToASCII(string(body)))
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

func (c *TailscaleClient) redactError(err error) error {
	return c.redactErrorWith(err)
}

func (c *TailscaleClient) redactErrorWith(err error, extraTokens ...string) error {
	if err == nil {
		return nil
	}
	c.mu.Lock()
	currentToken := c.accessToken
	c.mu.Unlock()
	values := make([]string, 0, 3+len(extraTokens))
	values = append(values, c.clientID, c.clientSecret, currentToken)
	values = append(values, extraTokens...)
	secrets := make([]string, 0, len(values)*2)
	for _, value := range values {
		if value == "" {
			continue
		}
		secrets = append(secrets, value)
		if encoded := url.QueryEscape(value); encoded != value {
			secrets = append(secrets, encoded)
		}
	}
	message := sanitizeDoctorError(err, secrets)
	var unsupported *unsupportedPolicyError
	if errors.As(err, &unsupported) {
		section := ""
		if unsupported.Section != "" {
			section = sanitizeDoctorError(errors.New(unsupported.Section), secrets)
		}
		reason := sanitizeDoctorError(errors.New(unsupported.Reason), secrets)
		return &redactedError{
			message: message,
			cause:   &unsupportedPolicyError{Section: section, Reason: reason},
		}
	}
	return errors.New(message)
}
