package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type serviceProbeResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (function serviceProbeResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return function(ctx, network, host)
}

type serviceProbeDialerFunc func(context.Context, string, string) (net.Conn, error)

func (function serviceProbeDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return function(ctx, network, address)
}

func testServiceProbeConfig() *ServiceHealthConfig {
	return &ServiceHealthConfig{
		Timeout:            time.Second,
		AllowedCIDRs:       []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")},
		AllowedHosts:       []string{"app.internal", "npm.internal"},
		AllowedDNSSuffixes: []string{".example.com"},
	}
}

func testHTTPService() ServiceHealthService {
	return ServiceHealthService{
		ProxyHostID:      7,
		Type:             ServiceHealthProbeHTTP,
		Path:             "/health/live%20check",
		AcceptedStatuses: []ServiceHealthStatusRange{{Min: 200, Max: 299}},
	}
}

func testProxyHost() ProxyHost {
	return ProxyHost{
		ID:            7,
		ForwardScheme: "http",
		ForwardHost:   "app.internal",
		ForwardPort:   8080,
		Enabled:       true,
	}
}

func staticServiceProbeResolver(answers map[string][]netip.Addr) serviceProbeResolverFunc {
	return func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" {
			return nil, errors.New("unexpected network")
		}
		addresses, ok := answers[host]
		if !ok {
			return nil, errors.New("not found")
		}
		return append([]netip.Addr(nil), addresses...), nil
	}
}

func TestDeriveServiceProbeTargetUsesOnlyNPMBackendAndProbePath(t *testing.T) {
	proxyHost := testProxyHost()
	proxyHost.Meta.NginxOnline = false
	service := testHTTPService()
	target, ok := deriveServiceProbeTarget(proxyHost, service)
	if !ok {
		t.Fatal("deriveServiceProbeTarget() rejected valid target")
	}
	if target.proxyHostID != proxyHost.ID || target.scheme != "http" || target.host != "app.internal" || target.port != 8080 || target.path != service.Path {
		t.Fatalf("target = %#v", target)
	}

	proxyHost.Meta.NginxOnline = true
	targetWithDifferentMetadata, ok := deriveServiceProbeTarget(proxyHost, service)
	if !ok || !reflect.DeepEqual(target, targetWithDifferentMetadata) {
		t.Fatalf("NPM metadata changed target: %#v != %#v", target, targetWithDifferentMetadata)
	}

	for _, mutate := range []func(*ProxyHost, *ServiceHealthService){
		func(host *ProxyHost, _ *ServiceHealthService) { host.Enabled = false },
		func(host *ProxyHost, _ *ServiceHealthService) { host.ID = 8 },
		func(host *ProxyHost, _ *ServiceHealthService) { host.ForwardScheme = "ftp" },
		func(host *ProxyHost, _ *ServiceHealthService) { host.ForwardHost = " app.internal" },
		func(host *ProxyHost, _ *ServiceHealthService) { host.ForwardPort = 0 },
		func(_ *ProxyHost, service *ServiceHealthService) { service.Path = "https://metadata.example/health" },
	} {
		hostCopy, serviceCopy := testProxyHost(), testHTTPService()
		mutate(&hostCopy, &serviceCopy)
		if _, ok := deriveServiceProbeTarget(hostCopy, serviceCopy); ok {
			t.Fatalf("deriveServiceProbeTarget() accepted host=%#v service=%#v", hostCopy, serviceCopy)
		}
	}
}

func TestServiceProbeAllowlistExactSuffixAndCIDR(t *testing.T) {
	engine, err := newServiceProbeEngine(testServiceProbeConfig(), nil, serviceProbeDependencies{
		resolver: staticServiceProbeResolver(map[string][]netip.Addr{
			"app.internal":       {netip.MustParseAddr("10.1.2.3")},
			"api.example.com":    {netip.MustParseAddr("10.2.3.4")},
			"example.com":        {netip.MustParseAddr("10.3.4.5")},
			"denied.example.com": {netip.MustParseAddr("192.168.1.5")},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		host string
		want bool
	}{
		{host: "app.internal", want: true},
		{host: "api.example.com", want: true},
		{host: "example.com", want: false},
		{host: "other.internal", want: false},
		{host: "10.9.8.7", want: true},
		{host: "192.168.1.5", want: false},
		{host: "denied.example.com", want: false},
	} {
		t.Run(test.host, func(t *testing.T) {
			_, err := engine.resolveAllowedTarget(context.Background(), test.host)
			if (err == nil) != test.want {
				t.Fatalf("resolveAllowedTarget(%q) error = %v, want allowed %t", test.host, err, test.want)
			}
		})
	}
}

func TestServiceProbeRejectsMixedDNSAndDangerousAnswers(t *testing.T) {
	answers := map[string][]netip.Addr{
		"mixed.example.com":       {netip.MustParseAddr("10.1.1.1"), netip.MustParseAddr("192.168.1.1")},
		"unspecified.example.com": {netip.MustParseAddr("0.0.0.0")},
		"loopback.example.com":    {netip.MustParseAddr("127.0.0.1")},
		"multicast.example.com":   {netip.MustParseAddr("224.0.0.1")},
		"linklocal.example.com":   {netip.MustParseAddr("169.254.1.1")},
		"broadcast.example.com":   {netip.MustParseAddr("255.255.255.255")},
	}
	config := testServiceProbeConfig()
	engine, err := newServiceProbeEngine(config, nil, serviceProbeDependencies{resolver: staticServiceProbeResolver(answers)})
	if err != nil {
		t.Fatal(err)
	}
	for host := range answers {
		if _, err := engine.resolveAllowedTarget(context.Background(), host); err == nil {
			t.Errorf("resolveAllowedTarget(%q) accepted denied answer set", host)
		}
	}
}

func TestServiceProbeUnmapsIPv4MappedAnswersAndDialsOnceWithoutRebinding(t *testing.T) {
	var lookups atomic.Int32
	var dialed string
	resolver := serviceProbeResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("::ffff:10.4.5.6")}, nil
	})
	dialer := serviceProbeDialerFunc(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		client, server := net.Pipe()
		go func() {
			server.Close()
		}()
		return client, nil
	})
	engine, err := newServiceProbeEngine(testServiceProbeConfig(), nil, serviceProbeDependencies{resolver: resolver, dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	service := ServiceHealthService{ProxyHostID: 7, Type: ServiceHealthProbeTCP}
	result := engine.Probe(context.Background(), testProxyHost(), service)
	if result.State != ServiceHealthStateReachable {
		t.Fatalf("Probe() state = %q", result.State)
	}
	if lookups.Load() != 1 {
		t.Fatalf("DNS lookups = %d, want 1", lookups.Load())
	}
	if dialed != "10.4.5.6:8080" {
		t.Fatalf("dialed address = %q", dialed)
	}
}

func TestServiceProbeProtectsExactAndAliasedAPISockets(t *testing.T) {
	answers := map[string][]netip.Addr{
		"app.internal": {netip.MustParseAddr("10.1.1.1")},
		"npm.internal": {netip.MustParseAddr("10.1.1.1")},
	}
	var dials atomic.Int32
	engine, err := newServiceProbeEngine(testServiceProbeConfig(), []string{"https://npm.internal:8080/api"}, serviceProbeDependencies{
		resolver: staticServiceProbeResolver(answers),
		dialer: serviceProbeDialerFunc(func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("must not dial")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := ServiceHealthService{ProxyHostID: 7, Type: ServiceHealthProbeTCP}
	result := engine.Probe(context.Background(), testProxyHost(), service)
	if result.State != ServiceHealthStateUnreachable || dials.Load() != 0 {
		t.Fatalf("aliased protected Probe() = %#v, dials = %d", result, dials.Load())
	}

	exactHost := testProxyHost()
	exactHost.ForwardHost = "npm.internal"
	result = engine.Probe(context.Background(), exactHost, service)
	if result.State != ServiceHealthStateUnreachable || dials.Load() != 0 {
		t.Fatalf("exact protected Probe() = %#v, dials = %d", result, dials.Load())
	}

	differentPort := testProxyHost()
	differentPort.ForwardPort = 8081
	engine.dialer = serviceProbeDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go server.Close()
		return client, nil
	})
	result = engine.Probe(context.Background(), differentPort, service)
	if result.State != ServiceHealthStateReachable {
		t.Fatalf("different-port Probe() state = %q", result.State)
	}
}

func TestServiceProbeHTTPGETHostPathAndStatusStates(t *testing.T) {
	statuses := []struct {
		name  string
		code  int
		state ServiceHealthState
		class int
	}{
		{name: "accepted", code: 204, state: ServiceHealthStateReachable, class: 2},
		{name: "unauthorized", code: 401, state: ServiceHealthStateAuthRequired, class: 4},
		{name: "forbidden", code: 403, state: ServiceHealthStateAuthRequired, class: 4},
		{name: "response error", code: 500, state: ServiceHealthStateResponseError, class: 5},
	}
	for _, test := range statuses {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.Host != "app.internal:8080" || request.URL.RequestURI() != "/health/live%20check" {
					t.Errorf("request = %s host=%q uri=%q", request.Method, request.Host, request.URL.RequestURI())
				}
				writer.WriteHeader(test.code)
			}))
			defer server.Close()
			engine := testServiceProbeEngineDialingServer(t, server, nil)
			result := engine.Probe(context.Background(), testProxyHost(), testHTTPService())
			if result.State != test.state || result.HTTPStatusClass != test.class || result.ProxyHostID != 7 || result.CheckedAt.IsZero() || result.Duration < 0 {
				t.Fatalf("Probe() = %#v", result)
			}
		})
	}
}

func TestServiceProbeHTTPDoesNotFollowRedirectsOrUseEnvironmentProxy(t *testing.T) {
	var redirected atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer redirectTarget.Close()

	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxied.Add(1)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, redirectTarget.URL, http.StatusFound)
	}))
	defer server.Close()
	engine := testServiceProbeEngineDialingServer(t, server, nil)
	result := engine.Probe(context.Background(), testProxyHost(), testHTTPService())
	if result.State != ServiceHealthStateResponseError || result.HTTPStatusClass != 3 {
		t.Fatalf("Probe() = %#v", result)
	}
	if redirected.Load() != 0 || proxied.Load() != 0 {
		t.Fatalf("redirected = %d, proxied = %d", redirected.Load(), proxied.Load())
	}
}

func TestServiceProbeHTTPTLSPreservesSNIAndRequiresTLS12(t *testing.T) {
	var serverName string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverName = request.TLS.ServerName
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	engine := testServiceProbeEngineDialingServer(t, server, roots)
	proxyHost := testProxyHost()
	proxyHost.ForwardScheme = "https"
	proxyHost.ForwardHost = "example.com"
	proxyHost.ForwardPort = 443
	result := engine.Probe(context.Background(), proxyHost, testHTTPService())
	if result.State != ServiceHealthStateReachable || serverName != "example.com" {
		t.Fatalf("Probe() = %#v, SNI = %q", result, serverName)
	}

	oldTLS := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	oldTLS.TLS = &tls.Config{MaxVersion: tls.VersionTLS11}
	oldTLS.StartTLS()
	defer oldTLS.Close()
	oldRoots := x509.NewCertPool()
	oldRoots.AddCert(oldTLS.Certificate())
	engine = testServiceProbeEngineDialingServer(t, oldTLS, oldRoots)
	result = engine.Probe(context.Background(), proxyHost, testHTTPService())
	if result.State != ServiceHealthStateUnreachable || result.HTTPStatusClass != 0 {
		t.Fatalf("TLS 1.1 Probe() = %#v", result)
	}
}

func TestServiceProbeHTTPRejectsOversizedResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Oversized", strings.Repeat("x", maxServiceProbeResponseHeaders+1))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	engine := testServiceProbeEngineDialingServer(t, server, nil)
	result := engine.Probe(context.Background(), testProxyHost(), testHTTPService())
	if result.State != ServiceHealthStateUnreachable || result.HTTPStatusClass != 0 {
		t.Fatalf("Probe() = %#v", result)
	}
}

func TestServiceProbeTCPConnectsAndClosesWithoutPayload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	readResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			readResult <- acceptErr
			return
		}
		defer connection.Close()
		connection.SetReadDeadline(time.Now().Add(time.Second))
		buffer := make([]byte, 1)
		count, readErr := connection.Read(buffer)
		if count != 0 {
			readResult <- errors.New("TCP probe sent payload")
			return
		}
		if readErr != io.EOF {
			readResult <- readErr
			return
		}
		readResult <- nil
	}()

	dialer := serviceProbeDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	})
	engine, err := newServiceProbeEngine(testServiceProbeConfig(), nil, serviceProbeDependencies{
		resolver: staticServiceProbeResolver(map[string][]netip.Addr{"app.internal": {netip.MustParseAddr("10.1.2.3")}}),
		dialer:   dialer,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := ServiceHealthService{ProxyHostID: 7, Type: ServiceHealthProbeTCP}
	result := engine.Probe(context.Background(), testProxyHost(), service)
	if result.State != ServiceHealthStateReachable || result.HTTPStatusClass != 0 {
		t.Fatalf("Probe() = %#v", result)
	}
	if err := <-readResult; err != nil {
		t.Fatalf("TCP server read = %v", err)
	}
}

func TestServiceProbeResultRetainsOnlyCoarseFields(t *testing.T) {
	resultType := reflect.TypeOf(ServiceHealthResult{})
	wantFields := []string{"ProxyHostID", "State", "CheckedAt", "Duration", "HTTPStatusClass"}
	if resultType.NumField() != len(wantFields) {
		t.Fatalf("ServiceHealthResult fields = %d, want %d", resultType.NumField(), len(wantFields))
	}
	for index, want := range wantFields {
		if got := resultType.Field(index).Name; got != want {
			t.Fatalf("ServiceHealthResult field %d = %q, want %q", index, got, want)
		}
	}
}

func testServiceProbeEngineDialingServer(t *testing.T, server *httptest.Server, roots *x509.CertPool) *serviceProbeEngine {
	t.Helper()
	serverAddress := server.Listener.Addr().String()
	config := testServiceProbeConfig()
	config.AllowedHosts = append(config.AllowedHosts, "example.com")
	engine, err := newServiceProbeEngine(config, nil, serviceProbeDependencies{
		resolver: staticServiceProbeResolver(map[string][]netip.Addr{
			"app.internal": {netip.MustParseAddr("10.1.2.3")},
			"example.com":  {netip.MustParseAddr("10.1.2.4")},
		}),
		dialer: serviceProbeDialerFunc(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
		}),
		rootCAs: roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestServiceProbeTimeoutIsTotalAndTimeIsInjectable(t *testing.T) {
	config := testServiceProbeConfig()
	config.Timeout = 20 * time.Millisecond
	times := []time.Time{time.Unix(100, 0), time.Unix(100, int64(12*time.Millisecond))}
	var timeIndex int
	engine, err := newServiceProbeEngine(config, nil, serviceProbeDependencies{
		resolver: staticServiceProbeResolver(map[string][]netip.Addr{"app.internal": {netip.MustParseAddr("10.1.2.3")}}),
		dialer: serviceProbeDialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		now: func() time.Time {
			value := times[timeIndex]
			timeIndex++
			return value
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := ServiceHealthService{ProxyHostID: 7, Type: ServiceHealthProbeTCP}
	result := engine.Probe(context.Background(), testProxyHost(), service)
	if result.State != ServiceHealthStateUnreachable || !result.CheckedAt.Equal(times[1]) || result.Duration != 12*time.Millisecond {
		t.Fatalf("Probe() = %#v", result)
	}
}
