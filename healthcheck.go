package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHealthcheckURL     = "http://127.0.0.1:8080/healthz"
	defaultHealthcheckTimeout = 3 * time.Second
	maxHealthcheckHeaders     = 16 * 1024
)

const healthcheckUsage = `Usage:
  velociportal healthcheck [--url URL] [--timeout DURATION]

Options:
  --url URL           Health endpoint to probe (default http://127.0.0.1:8080/healthz)
  --timeout DURATION  Total request timeout (default 3s)
  -h, --help          Show this help
`

// runHealthcheckCommand probes only the supplied health URL. It deliberately
// does not load application configuration or attach application credentials.
func runHealthcheckCommand(args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, healthcheckUsage)
		return 0
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(stderr, "velociportal: healthcheck: %s cannot be combined with other arguments\n\n%s", arg, healthcheckUsage)
			return 2
		}
	}

	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := defaultHealthcheckURL
	timeout := defaultHealthcheckTimeout
	flags.StringVar(&target, "url", target, "health endpoint to probe")
	flags.DurationVar(&timeout, "timeout", timeout, "total request timeout")
	flags.Usage = func() {}

	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(stderr, "velociportal: healthcheck: %v\n\n%s", err, healthcheckUsage)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "velociportal: healthcheck does not accept positional arguments\n\n%s", healthcheckUsage)
		return 2
	}
	if timeout <= 0 {
		fmt.Fprintf(stderr, "velociportal: healthcheck: --timeout must be greater than zero\n\n%s", healthcheckUsage)
		return 2
	}

	parsed, err := validateHealthcheckURL(target)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: healthcheck: invalid --url: %v\n\n%s", err, healthcheckUsage)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		fmt.Fprintf(stderr, "velociportal: healthcheck: create request: %v\n", err)
		return 1
	}
	request.Header.Set("User-Agent", "velociportal-healthcheck")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = maxHealthcheckHeaders

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	response, err := client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		fmt.Fprintf(stderr, "velociportal: healthcheck: request failed: %v\n", err)
		return 1
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "velociportal: healthcheck: unhealthy: HTTP %s\n", response.Status)
		return 1
	}

	fmt.Fprintln(stdout, "healthy")
	return 0
}

func validateHealthcheckURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("URL must not be empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("absolute URL with a host is required")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("user information is not allowed")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return nil, fmt.Errorf("port must not be empty")
	}
	if portText := parsed.Port(); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("port must be a number from 1 to 65535")
		}
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("fragment is not allowed")
	}
	return parsed, nil
}
