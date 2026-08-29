package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxServiceProbeResponseHeaders = 16 * 1024

type ServiceHealthState string

const (
	ServiceHealthStateUnknown       ServiceHealthState = "unknown"
	ServiceHealthStateReachable     ServiceHealthState = "reachable"
	ServiceHealthStateAuthRequired  ServiceHealthState = "auth_required"
	ServiceHealthStateResponseError ServiceHealthState = "response_error"
	ServiceHealthStateUnreachable   ServiceHealthState = "unreachable"
)

type ServiceHealthResult struct {
	ProxyHostID     int
	State           ServiceHealthState
	CheckedAt       time.Time
	Duration        time.Duration
	HTTPStatusClass int
}

type serviceProbeResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type serviceProbeDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type serviceProbeDependencies struct {
	resolver serviceProbeResolver
	dialer   serviceProbeDialer
	now      func() time.Time
	rootCAs  *x509.CertPool
}

type serviceProbeEngine struct {
	timeout            time.Duration
	allowedCIDRs       []netip.Prefix
	allowedHosts       map[string]struct{}
	allowedDNSSuffixes []string
	protected          []serviceProbeProtectedEndpoint
	resolver           serviceProbeResolver
	dialer             serviceProbeDialer
	now                func() time.Time
	rootCAs            *x509.CertPool
}

type serviceProbeProtectedEndpoint struct {
	host string
	port uint16
}

type serviceProbeTarget struct {
	proxyHostID      int
	probeType        ServiceHealthProbeType
	scheme           string
	host             string
	canonicalHost    string
	port             uint16
	authority        string
	path             string
	acceptedStatuses []ServiceHealthStatusRange
}

func newServiceProbeEngine(config *ServiceHealthConfig, protectedURLs []string, dependencyOverrides ...serviceProbeDependencies) (*serviceProbeEngine, error) {
	if config == nil {
		return nil, errors.New("service probe configuration is required")
	}
	dependencies := serviceProbeDependencies{}
	if len(dependencyOverrides) > 1 {
		return nil, errors.New("service probe dependencies are invalid")
	}
	if len(dependencyOverrides) == 1 {
		dependencies = dependencyOverrides[0]
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultServiceHealthTimeout
	}
	if dependencies.resolver == nil {
		dependencies.resolver = net.DefaultResolver
	}
	if dependencies.dialer == nil {
		dependencies.dialer = &net.Dialer{Timeout: timeout, KeepAlive: -1}
	}
	if dependencies.now == nil {
		dependencies.now = time.Now
	}

	protected := make([]serviceProbeProtectedEndpoint, 0, len(protectedURLs))
	for index, rawURL := range protectedURLs {
		endpoint, err := parseServiceProbeProtectedEndpoint(rawURL)
		if err != nil {
			return nil, errors.New("service probe protected URL " + strconv.Itoa(index) + " is invalid")
		}
		protected = append(protected, endpoint)
	}

	allowedHosts := make(map[string]struct{}, len(config.AllowedHosts))
	for _, host := range config.AllowedHosts {
		allowedHosts[host] = struct{}{}
	}

	return &serviceProbeEngine{
		timeout:            timeout,
		allowedCIDRs:       append([]netip.Prefix(nil), config.AllowedCIDRs...),
		allowedHosts:       allowedHosts,
		allowedDNSSuffixes: append([]string(nil), config.AllowedDNSSuffixes...),
		protected:          protected,
		resolver:           dependencies.resolver,
		dialer:             dependencies.dialer,
		now:                dependencies.now,
		rootCAs:            dependencies.rootCAs,
	}, nil
}

func parseServiceProbeProtectedEndpoint(rawURL string) (serviceProbeProtectedEndpoint, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return serviceProbeProtectedEndpoint{}, errors.New("invalid protected endpoint")
	}
	portValue := parsed.Port()
	if portValue == "" {
		if parsed.Scheme == "https" {
			portValue = "443"
		} else {
			portValue = "80"
		}
	}
	port, err := strconv.ParseUint(portValue, 10, 16)
	if err != nil || port == 0 {
		return serviceProbeProtectedEndpoint{}, errors.New("invalid protected endpoint")
	}
	host, err := canonicalServiceProbeHost(parsed.Hostname())
	if err != nil {
		return serviceProbeProtectedEndpoint{}, errors.New("invalid protected endpoint")
	}
	return serviceProbeProtectedEndpoint{host: host, port: uint16(port)}, nil
}

func deriveServiceProbeTarget(proxyHost ProxyHost, service ServiceHealthService) (serviceProbeTarget, bool) {
	if !proxyHost.Enabled || proxyHost.ID <= 0 || proxyHost.ID != service.ProxyHostID || proxyHost.ForwardPort <= 0 || proxyHost.ForwardPort > 65535 {
		return serviceProbeTarget{}, false
	}
	scheme := strings.ToLower(proxyHost.ForwardScheme)
	if scheme != "http" && scheme != "https" {
		return serviceProbeTarget{}, false
	}
	canonicalHost, err := canonicalServiceProbeHost(proxyHost.ForwardHost)
	if err != nil {
		return serviceProbeTarget{}, false
	}

	target := serviceProbeTarget{
		proxyHostID:      proxyHost.ID,
		probeType:        service.Type,
		scheme:           scheme,
		host:             proxyHost.ForwardHost,
		canonicalHost:    canonicalHost,
		port:             uint16(proxyHost.ForwardPort),
		authority:        net.JoinHostPort(proxyHost.ForwardHost, strconv.Itoa(proxyHost.ForwardPort)),
		acceptedStatuses: append([]ServiceHealthStatusRange(nil), service.AcceptedStatuses...),
	}
	switch service.Type {
	case ServiceHealthProbeHTTP:
		if validateServiceHealthPath(service.Path) != nil || !validServiceProbeStatusRanges(service.AcceptedStatuses) {
			return serviceProbeTarget{}, false
		}
		target.path = service.Path
	case ServiceHealthProbeTCP:
		if service.Path != "" || len(service.AcceptedStatuses) != 0 {
			return serviceProbeTarget{}, false
		}
	default:
		return serviceProbeTarget{}, false
	}
	return target, true
}

func validServiceProbeStatusRanges(ranges []ServiceHealthStatusRange) bool {
	if len(ranges) == 0 || len(ranges) > maxServiceHealthStatusRanges {
		return false
	}
	for index, statusRange := range ranges {
		if statusRange.Min < 100 || statusRange.Max > 599 || statusRange.Min > statusRange.Max {
			return false
		}
		if index > 0 && statusRange.Min <= ranges[index-1].Max {
			return false
		}
	}
	return true
}

func canonicalServiceProbeHost(rawHost string) (string, error) {
	if rawHost == "" || strings.TrimSpace(rawHost) != rawHost {
		return "", errors.New("invalid host")
	}
	if address, err := netip.ParseAddr(rawHost); err == nil {
		if address.Zone() != "" {
			return "", errors.New("invalid host")
		}
		return address.Unmap().String(), nil
	}
	host := strings.ToLower(rawHost)
	if validateServiceHealthHost(host) != nil {
		return "", errors.New("invalid host")
	}
	return host, nil
}

func (engine *serviceProbeEngine) Probe(ctx context.Context, proxyHost ProxyHost, service ServiceHealthService) ServiceHealthResult {
	start := engine.now()
	result := ServiceHealthResult{ProxyHostID: service.ProxyHostID, State: ServiceHealthStateUnknown}

	target, ok := deriveServiceProbeTarget(proxyHost, service)
	if !ok {
		return engine.finishResult(result, start)
	}
	result.ProxyHostID = target.proxyHostID

	probeContext, cancel := context.WithTimeout(ctx, engine.timeout)
	defer cancel()
	addresses, err := engine.validateTarget(probeContext, target)
	if err != nil {
		result.State = ServiceHealthStateUnreachable
		return engine.finishResult(result, start)
	}

	switch target.probeType {
	case ServiceHealthProbeHTTP:
		result.State, result.HTTPStatusClass = engine.probeHTTP(probeContext, target, addresses)
	case ServiceHealthProbeTCP:
		result.State = engine.probeTCP(probeContext, target, addresses)
	}
	return engine.finishResult(result, start)
}

func (engine *serviceProbeEngine) finishResult(result ServiceHealthResult, start time.Time) ServiceHealthResult {
	result.CheckedAt = engine.now()
	result.Duration = result.CheckedAt.Sub(start)
	if result.Duration < 0 {
		result.Duration = 0
	}
	return result
}

func (engine *serviceProbeEngine) validateTarget(ctx context.Context, target serviceProbeTarget) ([]netip.Addr, error) {
	if engine.isProtectedExact(target.canonicalHost, target.port) {
		return nil, errors.New("protected socket")
	}

	addresses, err := engine.resolveAllowedTarget(ctx, target.canonicalHost)
	if err != nil {
		return nil, err
	}
	for _, endpoint := range engine.protected {
		if endpoint.port != target.port {
			continue
		}
		protectedAddresses, err := engine.resolveHost(ctx, endpoint.host)
		if err != nil {
			return nil, errors.New("protected socket could not be resolved")
		}
		if serviceProbeAddressSetsIntersect(addresses, protectedAddresses) {
			return nil, errors.New("protected socket")
		}
	}
	return addresses, nil
}

func (engine *serviceProbeEngine) isProtectedExact(host string, port uint16) bool {
	for _, endpoint := range engine.protected {
		if endpoint.host == host && endpoint.port == port {
			return true
		}
	}
	return false
}

func (engine *serviceProbeEngine) resolveAllowedTarget(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !engine.addressAllowed(address) {
			return nil, errors.New("address is denied")
		}
		return []netip.Addr{address}, nil
	}
	if !engine.dnsNameAllowed(host) {
		return nil, errors.New("DNS name is denied")
	}
	addresses, err := engine.resolveHost(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if !engine.addressAllowed(address) {
			return nil, errors.New("DNS answer set contains a denied address")
		}
	}
	return addresses, nil
}

func (engine *serviceProbeEngine) resolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if serviceProbeDangerousAddress(address) {
			return nil, errors.New("dangerous address")
		}
		return []netip.Addr{address}, nil
	}
	addresses, err := engine.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("DNS resolution failed")
	}
	unique := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		if !address.IsValid() || address.Zone() != "" {
			return nil, errors.New("DNS resolution returned an invalid address")
		}
		address = address.Unmap()
		if serviceProbeDangerousAddress(address) {
			return nil, errors.New("DNS resolution returned a dangerous address")
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		unique = append(unique, address)
	}
	if len(unique) == 0 {
		return nil, errors.New("DNS resolution returned no addresses")
	}
	return unique, nil
}

func (engine *serviceProbeEngine) dnsNameAllowed(host string) bool {
	if _, ok := engine.allowedHosts[host]; ok {
		return true
	}
	for _, suffix := range engine.allowedDNSSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func (engine *serviceProbeEngine) addressAllowed(address netip.Addr) bool {
	address = address.Unmap()
	if serviceProbeDangerousAddress(address) {
		return false
	}
	for _, prefix := range engine.allowedCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func serviceProbeDangerousAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" || serviceHealthDangerousAddress(address) {
		return true
	}
	return address.Is4() && address == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

func serviceProbeAddressSetsIntersect(left, right []netip.Addr) bool {
	addresses := make(map[netip.Addr]struct{}, len(left))
	for _, address := range left {
		addresses[address.Unmap()] = struct{}{}
	}
	for _, address := range right {
		if _, ok := addresses[address.Unmap()]; ok {
			return true
		}
	}
	return false
}

func (engine *serviceProbeEngine) probeTCP(ctx context.Context, target serviceProbeTarget, addresses []netip.Addr) ServiceHealthState {
	connection, err := engine.dialValidated(ctx, "tcp", target.port, addresses)
	if err != nil {
		return ServiceHealthStateUnreachable
	}
	_ = connection.Close()
	return ServiceHealthStateReachable
}

func (engine *serviceProbeEngine) probeHTTP(ctx context.Context, target serviceProbeTarget, addresses []netip.Addr) (ServiceHealthState, int) {
	requestURL, err := url.Parse(target.scheme + "://" + target.authority + target.path)
	if err != nil {
		return ServiceHealthStateUnknown, 0
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: engine.rootCAs}
	if _, err := netip.ParseAddr(target.canonicalHost); err != nil {
		tlsConfig.ServerName = target.host
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialContext context.Context, network, _ string) (net.Conn, error) {
			return engine.dialValidated(dialContext, network, target.port, addresses)
		},
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      false,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    engine.timeout,
		ResponseHeaderTimeout:  engine.timeout,
		MaxResponseHeaderBytes: maxServiceProbeResponseHeaders,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   engine.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return ServiceHealthStateUnknown, 0
	}
	request.Host = target.authority
	response, err := client.Do(request)
	if err != nil {
		return ServiceHealthStateUnreachable, 0
	}
	response.Body.Close()

	statusClass := response.StatusCode / 100
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ServiceHealthStateAuthRequired, statusClass
	}
	for _, accepted := range target.acceptedStatuses {
		if response.StatusCode >= accepted.Min && response.StatusCode <= accepted.Max {
			return ServiceHealthStateReachable, statusClass
		}
	}
	return ServiceHealthStateResponseError, statusClass
}

func (engine *serviceProbeEngine) dialValidated(ctx context.Context, network string, port uint16, addresses []netip.Addr) (net.Conn, error) {
	var lastError error
	for _, address := range addresses {
		connection, err := engine.dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), strconv.Itoa(int(port))))
		if err == nil {
			return connection, nil
		}
		lastError = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastError == nil {
		lastError = errors.New("no validated addresses")
	}
	return nil, lastError
}
