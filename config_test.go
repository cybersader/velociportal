package main

import (
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func validConfigValues() map[string]string {
	return map[string]string{
		"HEADSCALE_URL":      "https://headscale:8080",
		"HEADSCALE_API_KEY":  "test-key",
		"NPM_URL":            "http://npm.velociportal.internal:81",
		"NPM_EMAIL":          "admin@example.com",
		"NPM_PASSWORD":       "changeme",
		"TRUSTED_PROXY_CIDR": "127.0.0.1/32",
	}
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func TestLoadConfigFromValidAndNormalized(t *testing.T) {
	values := validConfigValues()
	values["HEADSCALE_URL"] = " https://headscale.example.com/control/ "
	values["NPM_URL"] = "http://npm.velociportal.internal:81///"
	values["NPM_EMAIL"] = " admin@example.com "
	values["LISTEN_ADDR"] = " LOCALHOST:9090 "
	values["POLL_INTERVAL"] = " 1m "
	values["TRUSTED_PROXY_CIDR"] = " 100.64.2.3/10 "

	cfg, err := loadConfigFrom(mapConfigLookup(values))
	if err != nil {
		t.Fatalf("loadConfigFrom() error = %v", err)
	}

	if cfg.HeadscaleURL != "https://headscale.example.com/control" {
		t.Errorf("HeadscaleURL = %q", cfg.HeadscaleURL)
	}
	if cfg.NPMURL != "http://npm.velociportal.internal:81" {
		t.Errorf("NPMURL = %q", cfg.NPMURL)
	}
	if cfg.HeadscaleAPIKey != "test-key" {
		t.Errorf("HeadscaleAPIKey = %q", cfg.HeadscaleAPIKey)
	}
	if cfg.NPMEmail != "admin@example.com" {
		t.Errorf("NPMEmail = %q", cfg.NPMEmail)
	}
	if cfg.NPMPassword != "changeme" {
		t.Errorf("NPMPassword = %q", cfg.NPMPassword)
	}
	if cfg.ListenAddr != "localhost:9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.PollInterval != time.Minute {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.TrustedProxyCIDR.String() != "100.64.0.0/10" {
		t.Errorf("TrustedProxyCIDR = %v", cfg.TrustedProxyCIDR)
	}
}

func TestNormalizeBaseURLPreservesEscapedPathData(t *testing.T) {
	got, err := normalizeBaseURL("TEST_URL", "https://example.com/base/%2F/")
	if err != nil {
		t.Fatalf("normalizeBaseURL() error = %v", err)
	}
	if got != "https://example.com/base/%2F" {
		t.Fatalf("normalizeBaseURL() = %q, want escaped slash preserved", got)
	}
}

func TestNormalizeHeadscaleBaseURLAllowsVerifiedHTTPSGenerally(t *testing.T) {
	for _, input := range []string{
		"https://headscale.example.com:443/control/",
		"HTTPS://HEADSCALE",
		"https://10.0.0.2:8080",
		"https://headscale.velociportal.internal.evil.example",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeHeadscaleBaseURL(input); err != nil {
				t.Fatalf("normalizeHeadscaleBaseURL(%q) error = %v", input, err)
			}
		})
	}
}

func TestLoadConfigFromAllowsRestrictedHeadscaleHTTPHosts(t *testing.T) {
	tests := map[string]string{
		"canonical internal":      "http://headscale.velociportal.internal:8080/control/",
		"canonical internal case": "HTTP://HEADSCALE.VELOCIPORTAL.INTERNAL:8080",
		"Docker host":             "http://host.docker.internal:8080",
		"Docker host case":        "http://HOST.DOCKER.INTERNAL:8080",
		"localhost":               "http://localhost:8080",
		"localhost case":          "http://LOCALHOST:8080",
		"IPv4 loopback first":     "http://127.0.0.0:8080",
		"IPv4 loopback range":     "http://127.255.255.255:8080",
		"IPv6 loopback":           "http://[::1]:8080",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			values := validConfigValues()
			values["HEADSCALE_URL"] = input
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if err != nil {
				t.Fatalf("loadConfigFrom() error = %v", err)
			}
			if classifyHeadscaleTransport(cfg.HeadscaleURL) != headscaleTransportRestrictedHTTP {
				t.Fatalf("Headscale transport for %q was not classified as restricted HTTP", cfg.HeadscaleURL)
			}
		})
	}
}

func TestNormalizeHeadscaleBaseURLRejectsOtherHTTPHosts(t *testing.T) {
	for _, input := range []string{
		"http://headscale:8080",
		"http://headscale.home:8080",
		"http://10.0.0.2:8080",
		"http://172.17.0.2:8080",
		"http://192.168.1.2:8080",
		"http://100.64.0.2:8080",
		"http://169.254.1.2:8080",
		"http://126.255.255.255:8080",
		"http://128.0.0.0:8080",
		"http://203.0.113.10:8080",
		"http://headscale.example.com:8080",
		"http://headscale.velociportal.internal.evil.example:8080",
		"http://evil-headscale.velociportal.internal:8080",
		"http://headscale.velociportal.internal.:8080",
		"http://host.docker.internal.evil.example:8080",
		"http://localhost.example:8080",
		"http://0.0.0.0:8080",
		"http://[::]:8080",
		"http://[::2]:8080",
		"http://[fe80::1]:8080",
		"http://[::ffff:127.0.0.1]:8080",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeHeadscaleBaseURL(input); err == nil {
				t.Fatalf("normalizeHeadscaleBaseURL(%q) accepted disallowed HTTP host", input)
			}
		})
	}
}

func TestNormalizeNPMBaseURLAllowsVerifiedHTTPSAndRestrictedHTTP(t *testing.T) {
	for _, input := range []string{
		"https://npm.example.com:443/",
		"HTTPS://NPM.EXAMPLE.COM",
		"http://npm.velociportal.internal:81/",
		"HTTP://NPM.VELOCIPORTAL.INTERNAL:81",
		"http://host.docker.internal:81",
		"http://localhost:81",
		"http://127.0.0.1:81",
		"http://[::1]:81",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeNPMBaseURL(input); err != nil {
				t.Fatalf("normalizeNPMBaseURL(%q) error = %v", input, err)
			}
		})
	}
}

func TestNormalizeNPMBaseURLRejectsOtherHTTPHosts(t *testing.T) {
	for _, input := range []string{
		"http://npm:81",
		"http://npm.home:81",
		"http://10.0.0.2:81",
		"http://172.17.0.2:81",
		"http://192.168.1.2:81",
		"http://100.64.0.2:81",
		"http://npm.example.com:81",
		"http://npm.velociportal.internal.evil.example:81",
		"http://evil-npm.velociportal.internal:81",
		"http://npm.velociportal.internal.:81",
		"http://0.0.0.0:81",
		"http://[::]:81",
		"http://[::ffff:127.0.0.1]:81",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalizeNPMBaseURL(input); err == nil {
				t.Fatalf("normalizeNPMBaseURL(%q) accepted disallowed HTTP host", input)
			}
		})
	}
}

func TestClassifyHeadscaleTransport(t *testing.T) {
	for _, test := range []struct {
		input string
		want  headscaleTransportClass
	}{
		{input: "https://headscale.example.com", want: headscaleTransportVerifiedHTTPS},
		{input: "HTTPS://headscale.example.com", want: headscaleTransportVerifiedHTTPS},
		{input: "http://localhost", want: headscaleTransportRestrictedHTTP},
		{input: "ftp://localhost", want: headscaleTransportUnknown},
		{input: "://invalid", want: headscaleTransportUnknown},
	} {
		if got := classifyHeadscaleTransport(test.input); got != test.want {
			t.Errorf("classifyHeadscaleTransport(%q) = %v, want %v", test.input, got, test.want)
		}
	}
}

func TestLoadConfigFromDefaults(t *testing.T) {
	cfg, err := loadConfigFrom(mapConfigLookup(validConfigValues()))
	if err != nil {
		t.Fatalf("loadConfigFrom() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:8080", cfg.ListenAddr)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
}

func TestLoadConfigFromUsesDeterministicLookupOrder(t *testing.T) {
	values := validConfigValues()
	var calls []string
	lookup := func(key string) (string, bool, error) {
		calls = append(calls, key)
		value, ok := values[key]
		return value, ok, nil
	}

	if _, err := loadConfigFrom(lookup); err != nil {
		t.Fatalf("loadConfigFrom() error = %v", err)
	}
	want := []string{
		"HEADSCALE_URL",
		"HEADSCALE_API_KEY",
		"NPM_URL",
		"NPM_EMAIL",
		"NPM_PASSWORD",
		"TRUSTED_PROXY_CIDR",
		"LISTEN_ADDR",
		"POLL_INTERVAL",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("lookup calls = %v, want %v", calls, want)
	}
}

func TestLoadConfigUsesProcessEnvironment(t *testing.T) {
	t.Setenv(processEnvEncodingKey, "")
	values := validConfigValues()
	values["LISTEN_ADDR"] = "127.0.0.1:9191"
	values["POLL_INTERVAL"] = "45s"
	for key, value := range values {
		t.Setenv(key, value)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9191" || cfg.PollInterval != 45*time.Second {
		t.Fatalf("loadConfig() = ListenAddr %q, PollInterval %v", cfg.ListenAddr, cfg.PollInterval)
	}
}

func TestLoadConfigDecodesRawComposeQuotedEnvironment(t *testing.T) {
	values := validConfigValues()
	values["NPM_PASSWORD"] = `price$HOME ${NAME} "quoted" \\ path`
	values["HEADSCALE_API_KEY"] = `key with spaces # and $`
	for key, value := range values {
		t.Setenv(key, strconv.Quote(value))
	}
	t.Setenv("LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv(processEnvEncodingKey, goQuotedEnvEncoding)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.NPMPassword != values["NPM_PASSWORD"] || cfg.HeadscaleAPIKey != values["HEADSCALE_API_KEY"] {
		t.Fatalf("quoted credentials were not decoded exactly")
	}
	if cfg.ListenAddr != "0.0.0.0:8080" {
		t.Fatalf("unquoted LISTEN_ADDR = %q", cfg.ListenAddr)
	}
}

func TestProcessConfigLookupUsesEnvFileValueGrammar(t *testing.T) {
	t.Setenv(processEnvEncodingKey, goQuotedEnvEncoding)
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "single quoted", raw: `'pa$$\\word'`, want: `pa$$\\word`},
		{name: "double quoted", raw: `"two words # literal"`, want: "two words # literal"},
		{name: "unquoted", raw: `value # comment`, want: "value"},
		{name: "quoted comment", raw: `'literal value' # comment`, want: "literal value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PROCESS_GRAMMAR_TEST", test.raw)
			got, ok, err := processConfigLookup("PROCESS_GRAMMAR_TEST")
			if err != nil {
				t.Fatalf("processConfigLookup() error = %v", err)
			}
			if !ok || got != test.want {
				t.Fatalf("processConfigLookup() = %q, %v; want %q, true", got, ok, test.want)
			}
		})
	}
}

func TestLoadConfigRejectsMalformedRawComposeValueWithoutExposingIt(t *testing.T) {
	values := validConfigValues()
	for key, value := range values {
		t.Setenv(key, value)
	}
	secret := `"private-value\q"`
	t.Setenv("NPM_PASSWORD", secret)
	t.Setenv(processEnvEncodingKey, goQuotedEnvEncoding)

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() accepted malformed encoded value")
	}
	if !strings.Contains(err.Error(), "invalid encoded environment value for NPM_PASSWORD") {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if strings.Contains(err.Error(), "private-value") {
		t.Fatalf("loadConfig() error exposed secret value: %v", err)
	}
}

func TestLoadConfigFromPropagatesOptionalLookupErrors(t *testing.T) {
	for _, failureKey := range []string{"LISTEN_ADDR", "POLL_INTERVAL"} {
		t.Run(failureKey, func(t *testing.T) {
			values := validConfigValues()
			lookup := func(key string) (string, bool, error) {
				if key == failureKey {
					return "", true, fmt.Errorf("decode %s", key)
				}
				value, ok := values[key]
				return value, ok, nil
			}
			_, err := loadConfigFrom(lookup)
			if err == nil || !strings.Contains(err.Error(), "decode "+failureKey) {
				t.Fatalf("loadConfigFrom() error = %v", err)
			}
		})
	}
}

func TestLoadConfigFromReportsMissingKeysInRequiredOrder(t *testing.T) {
	_, err := loadConfigFrom(mapConfigLookup(map[string]string{
		"HEADSCALE_API_KEY": " ",
		"NPM_EMAIL":         "admin@example.com",
	}))
	if err == nil {
		t.Fatal("loadConfigFrom() error = nil")
	}

	want := "loadConfig: missing required env: HEADSCALE_URL, HEADSCALE_API_KEY, NPM_URL, NPM_PASSWORD, TRUSTED_PROXY_CIDR"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestLoadConfigFromRejectsNilLookup(t *testing.T) {
	_, err := loadConfigFrom(nil)
	if err == nil || !strings.Contains(err.Error(), "nil environment lookup") {
		t.Fatalf("loadConfigFrom(nil) error = %v", err)
	}
}

func TestLoadConfigFromPollIntervalValidation(t *testing.T) {
	for _, interval := range []string{"0s", "-1s", "1ns", "1s", "25h", "not-a-duration"} {
		t.Run(interval, func(t *testing.T) {
			values := validConfigValues()
			values["POLL_INTERVAL"] = interval
			_, err := loadConfigFrom(mapConfigLookup(values))
			if err == nil {
				t.Fatalf("loadConfigFrom() accepted POLL_INTERVAL=%q", interval)
			}
		})
	}
}

func TestLoadConfigFromURLValidation(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "relative", key: "HEADSCALE_URL", value: "headscale:8080"},
		{name: "insecure headscale", key: "HEADSCALE_URL", value: "http://headscale.example.com"},
		{name: "unsupported scheme", key: "HEADSCALE_URL", value: "ftp://headscale.example.com"},
		{name: "missing host", key: "NPM_URL", value: "https:///api"},
		{name: "userinfo", key: "NPM_URL", value: "https://admin:secret@npm.example.com"},
		{name: "query", key: "HEADSCALE_URL", value: "https://headscale.example.com?debug=1"},
		{name: "empty query", key: "HEADSCALE_URL", value: "https://headscale.example.com?"},
		{name: "fragment", key: "NPM_URL", value: "https://npm.example.com/#fragment"},
		{name: "empty fragment", key: "NPM_URL", value: "https://npm.example.com/#"},
		{name: "invalid port", key: "NPM_URL", value: "http://npm.example.com:notaport"},
		{name: "empty port", key: "NPM_URL", value: "http://npm.example.com:"},
		{name: "zero port", key: "NPM_URL", value: "http://npm.example.com:0"},
		{name: "out of range port", key: "NPM_URL", value: "http://npm.example.com:65536"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validConfigValues()
			values[test.key] = test.value
			_, err := loadConfigFrom(mapConfigLookup(values))
			if err == nil {
				t.Fatalf("loadConfigFrom() accepted %s=%q", test.key, test.value)
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Fatalf("error %q does not name %s", err, test.key)
			}
		})
	}
}

func TestNormalizeListenAddr(t *testing.T) {
	valid := map[string]string{
		"127.0.0.1:8080":    "127.0.0.1:8080",
		"0.0.0.0:8080":      "0.0.0.0:8080",
		"LOCALHOST:08080":   "localhost:8080",
		"[::1]:8080":        "[::1]:8080",
		"[fe80::1%eth0]:81": "[fe80::1%eth0]:81",
	}
	for input, want := range valid {
		t.Run("valid "+input, func(t *testing.T) {
			got, err := normalizeListenAddr(input)
			if err != nil {
				t.Fatalf("normalizeListenAddr(%q) error = %v", input, err)
			}
			if got != want {
				t.Fatalf("normalizeListenAddr(%q) = %q, want %q", input, got, want)
			}
		})
	}

	invalid := []string{
		"127.0.0.1",
		":8080",
		"example.com:8080",
		"127.0.0.1:0",
		"127.0.0.1:65536",
		"127.0.0.1:http",
		"224.0.0.1:8080",
		"255.255.255.255:8080",
		"[127.0.0.1%eth0]:8080",
		"[fe80::1%]:8080",
		"[fe80::1%%eth0]:8080",
	}
	for _, input := range invalid {
		t.Run("invalid "+input, func(t *testing.T) {
			if _, err := normalizeListenAddr(input); err == nil {
				t.Fatalf("normalizeListenAddr(%q) error = nil", input)
			}
		})
	}
}

func TestRequiredConfigKeyOrderIsStable(t *testing.T) {
	want := []string{
		"HEADSCALE_URL",
		"HEADSCALE_API_KEY",
		"NPM_URL",
		"NPM_EMAIL",
		"NPM_PASSWORD",
		"TRUSTED_PROXY_CIDR",
	}
	if !reflect.DeepEqual(requiredConfigKeys, want) {
		t.Fatalf("requiredConfigKeys = %v, want %v", requiredConfigKeys, want)
	}
}

func TestLoadConfigFromRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	values := cloneStrings(validConfigValues())
	values["TRUSTED_PROXY_CIDR"] = "not-a-cidr"
	_, err := loadConfigFrom(mapConfigLookup(values))
	if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDR") {
		t.Fatalf("loadConfigFrom() error = %v", err)
	}
}

func TestLoadConfigFromRejectsWholeAddressFamilyTrustedProxyCIDR(t *testing.T) {
	for _, cidr := range []string{
		"0.0.0.0/0",
		"::/0",
		"::ffff:0:0/96",
	} {
		t.Run(cidr, func(t *testing.T) {
			values := cloneStrings(validConfigValues())
			values["TRUSTED_PROXY_CIDR"] = cidr
			_, err := loadConfigFrom(mapConfigLookup(values))
			if err == nil || !strings.Contains(err.Error(), "entire IPv4 or IPv6 address space") {
				t.Fatalf("loadConfigFrom() error = %v", err)
			}
		})
	}
}

func TestTrustedProxyCoversAddressSpaceAcceptsNarrowRanges(t *testing.T) {
	for _, cidr := range []string{
		"127.0.0.1/32",
		"10.0.0.0/8",
		"2001:db8::/32",
		"::ffff:0:0/97",
	} {
		t.Run(cidr, func(t *testing.T) {
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				t.Fatalf("net.ParseCIDR(%q) error = %v", cidr, err)
			}
			if trustedProxyCoversAddressSpace(network) {
				t.Fatalf("trustedProxyCoversAddressSpace(%q) = true", cidr)
			}
		})
	}
}
