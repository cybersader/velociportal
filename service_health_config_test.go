package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validServiceHealthDocument() string {
	return `{
		"version": 1,
		"allowed_cidrs": ["10.0.0.0/8", "2001:db8::/32"],
		"allowed_hosts": ["app.internal", "backend"],
		"allowed_dns_suffixes": [".example.com"],
		"services": [
			{"proxy_host_id": 1, "type": "http", "accepted_statuses": [{"min": 200, "max": 399}, {"min": 404, "max": 404}]},
			{"proxy_host_id": 2, "type": "tcp"}
		]
	}`
}

func TestParseServiceHealthConfigValidDefaults(t *testing.T) {
	config, err := parseServiceHealthConfig([]byte(validServiceHealthDocument()))
	if err != nil {
		t.Fatalf("parseServiceHealthConfig() error = %v", err)
	}
	if !config.Enabled {
		t.Fatal("Enabled = false")
	}
	if config.Interval != defaultServiceHealthInterval || config.Timeout != defaultServiceHealthTimeout || config.Workers != defaultServiceHealthWorkers {
		t.Fatalf("defaults = interval %v, timeout %v, workers %d", config.Interval, config.Timeout, config.Workers)
	}
	if got := config.AllowedCIDRs[0].String(); got != "10.0.0.0/8" {
		t.Fatalf("AllowedCIDRs[0] = %q", got)
	}
	if !reflect.DeepEqual(config.AllowedHosts, []string{"app.internal", "backend"}) {
		t.Fatalf("AllowedHosts = %#v", config.AllowedHosts)
	}
	if !reflect.DeepEqual(config.AllowedDNSSuffixes, []string{".example.com"}) {
		t.Fatalf("AllowedDNSSuffixes = %#v", config.AllowedDNSSuffixes)
	}
	if len(config.Services) != 2 {
		t.Fatalf("Services = %#v", config.Services)
	}
	httpService := config.Services[0]
	if httpService.ProxyHostID != 1 || httpService.Type != ServiceHealthProbeHTTP || httpService.Path != "/" {
		t.Fatalf("HTTP service = %#v", httpService)
	}
	if !reflect.DeepEqual(httpService.AcceptedStatuses, []ServiceHealthStatusRange{{Min: 200, Max: 399}, {Min: 404, Max: 404}}) {
		t.Fatalf("AcceptedStatuses = %#v", httpService.AcceptedStatuses)
	}
	tcpService := config.Services[1]
	if tcpService.ProxyHostID != 2 || tcpService.Type != ServiceHealthProbeTCP || tcpService.Path != "" || tcpService.AcceptedStatuses != nil {
		t.Fatalf("TCP service = %#v", tcpService)
	}
}

func TestParseServiceHealthConfigCustomSchedulingAndPath(t *testing.T) {
	document := `{
		"version":1,
		"interval":"15s",
		"timeout":"250ms",
		"workers":8,
		"allowed_cidrs":["0.0.0.0/0"],
		"services":[{"proxy_host_id":7,"type":"http","path":"/health/live%20check","accepted_statuses":[{"min":204,"max":204}]}]
	}`
	config, err := parseServiceHealthConfig([]byte(document))
	if err != nil {
		t.Fatalf("parseServiceHealthConfig() error = %v", err)
	}
	if config.Interval != 15*time.Second || config.Timeout != 250*time.Millisecond || config.Workers != 8 {
		t.Fatalf("custom scheduling = interval %v, timeout %v, workers %d", config.Interval, config.Timeout, config.Workers)
	}
	if config.Services[0].Path != "/health/live%20check" {
		t.Fatalf("Path = %q", config.Services[0].Path)
	}
}

func TestParseServiceHealthConfigRejectsStrictShapes(t *testing.T) {
	tests := []struct {
		name     string
		document string
		contains string
	}{
		{name: "empty", document: ``, contains: "empty"},
		{name: "null", document: `null`, contains: "invalid"},
		{name: "array root", document: `[]`, contains: "invalid"},
		{name: "wrong version", document: `{"version":2,"allowed_cidrs":["10.0.0.0/8"],"services":[]}`, contains: "version"},
		{name: "missing version", document: `{"allowed_cidrs":["10.0.0.0/8"],"services":[]}`, contains: "version"},
		{name: "missing cidrs", document: `{"version":1,"services":[]}`, contains: "allowed_cidrs"},
		{name: "null cidrs", document: `{"version":1,"allowed_cidrs":null,"services":[]}`, contains: "allowed_cidrs"},
		{name: "empty cidrs", document: `{"version":1,"allowed_cidrs":[],"services":[]}`, contains: "allowed_cidrs"},
		{name: "missing services", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"]}`, contains: "services"},
		{name: "null services", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":null}`, contains: "services"},
		{name: "unknown top field", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[],"other":true}`, contains: "invalid"},
		{name: "noncanonical top field", document: `{"Version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[]}`, contains: "invalid"},
		{name: "duplicate top field", document: `{"version":1,"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[]}`, contains: "duplicate field"},
		{name: "mixed-case top alias", document: `{"version":1,"Version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[]}`, contains: "duplicate field"},
		{name: "noncanonical service field", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"Proxy_Host_ID":1,"type":"tcp"}]}`, contains: "invalid"},
		{name: "mixed-case service alias", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"Proxy_Host_ID":2,"type":"tcp"}]}`, contains: "duplicate field"},
		{name: "mixed-case range alias", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"http","accepted_statuses":[{"min":200,"Min":201,"max":399}]}]}`, contains: "duplicate field"},
		{name: "trailing data", document: `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[]} {}`, contains: "trailing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServiceHealthConfig([]byte(test.document))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("parseServiceHealthConfig() error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestParseServiceHealthConfigRejectsSchedulingValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{name: "interval below minimum", field: "interval", value: `"14.999s"`},
		{name: "interval above maximum", field: "interval", value: `"24h1s"`},
		{name: "interval malformed", field: "interval", value: `"soon"`},
		{name: "interval padded", field: "interval", value: `" 60s"`},
		{name: "interval wrong type", field: "interval", value: `60`},
		{name: "timeout below minimum", field: "timeout", value: `"249ms"`},
		{name: "timeout above maximum", field: "timeout", value: `"10.001s"`},
		{name: "timeout malformed", field: "timeout", value: `"later"`},
		{name: "workers below minimum", field: "workers", value: `0`},
		{name: "workers above maximum", field: "workers", value: `9`},
		{name: "workers wrong type", field: "workers", value: `"4"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := fmt.Sprintf(`{"version":1,%q:%s,"allowed_cidrs":["10.0.0.0/8"],"services":[]}`, test.field, test.value)
			if _, err := parseServiceHealthConfig([]byte(document)); err == nil {
				t.Fatalf("parseServiceHealthConfig() accepted %s", test.name)
			}
		})
	}
}

func TestParseServiceHealthConfigRejectsInvalidServices(t *testing.T) {
	tests := []struct {
		name    string
		service string
	}{
		{name: "non-object", service: `true`},
		{name: "missing id", service: `{"type":"tcp"}`},
		{name: "zero id", service: `{"proxy_host_id":0,"type":"tcp"}`},
		{name: "missing type", service: `{"proxy_host_id":1}`},
		{name: "mixed-case type", service: `{"proxy_host_id":1,"type":"HTTP","accepted_statuses":[{"min":200,"max":399}]}`},
		{name: "unknown type", service: `{"proxy_host_id":1,"type":"udp"}`},
		{name: "tcp path", service: `{"proxy_host_id":1,"type":"tcp","path":"/"}`},
		{name: "tcp null path", service: `{"proxy_host_id":1,"type":"tcp","path":null}`},
		{name: "tcp statuses", service: `{"proxy_host_id":1,"type":"tcp","accepted_statuses":[]}`},
		{name: "http missing statuses", service: `{"proxy_host_id":1,"type":"http"}`},
		{name: "http null statuses", service: `{"proxy_host_id":1,"type":"http","accepted_statuses":null}`},
		{name: "http empty statuses", service: `{"proxy_host_id":1,"type":"http","accepted_statuses":[]}`},
		{name: "unknown service field", service: `{"proxy_host_id":1,"type":"tcp","other":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[` + test.service + `]}`
			if _, err := parseServiceHealthConfig([]byte(document)); err == nil {
				t.Fatalf("parseServiceHealthConfig() accepted %s", test.name)
			}
		})
	}

	duplicateIDs := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"tcp"},{"proxy_host_id":1,"type":"tcp"}]}`
	if _, err := parseServiceHealthConfig([]byte(duplicateIDs)); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("duplicate proxy_host_id error = %v", err)
	}
}

func TestParseServiceHealthConfigRejectsInvalidPaths(t *testing.T) {
	paths := []string{
		`""`,
		`"health"`,
		`" /health"`,
		`"/health?full=1"`,
		`"/health#ready"`,
		`"/health\nready"`,
		`"/health/../ready"`,
		`"/bad percent%zz"`,
	}
	for _, path := range paths {
		document := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"http","path":` + path + `,"accepted_statuses":[{"min":200,"max":399}]}]}`
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "path") {
			t.Errorf("path %s error = %v", path, err)
		}
	}

	longPath := "/" + strings.Repeat("x", maxServiceHealthPathLength)
	document := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"http","path":"` + longPath + `","accepted_statuses":[{"min":200,"max":399}]}]}`
	if _, err := parseServiceHealthConfig([]byte(document)); err == nil {
		t.Fatal("parseServiceHealthConfig() accepted overlong path")
	}
}

func TestParseServiceHealthConfigRejectsInvalidStatusRanges(t *testing.T) {
	ranges := []string{
		`[{"min":99,"max":200}]`,
		`[{"min":200,"max":600}]`,
		`[{"min":399,"max":200}]`,
		`[{"min":200}]`,
		`[{"min":200,"max":399,"other":1}]`,
		`[{"min":"200","max":399}]`,
		`[{"min":300,"max":399},{"min":200,"max":299}]`,
		`[{"min":200,"max":399},{"min":399,"max":499}]`,
		`[{"min":200,"max":399},{"min":200,"max":399}]`,
	}
	for _, statuses := range ranges {
		document := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"http","accepted_statuses":` + statuses + `}]}`
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "accepted_statuses") {
			t.Errorf("accepted_statuses %s error = %v", statuses, err)
		}
	}

	tooMany := make([]string, maxServiceHealthStatusRanges+1)
	for index := range tooMany {
		code := 100 + index*2
		tooMany[index] = fmt.Sprintf(`{"min":%d,"max":%d}`, code, code)
	}
	document := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"http","accepted_statuses":[` + strings.Join(tooMany, ",") + `]}]}`
	if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("status range limit error = %v", err)
	}
}

func TestParseServiceHealthConfigRejectsInvalidCIDRs(t *testing.T) {
	invalid := []string{
		"10.0.0.1/8",
		"2001:DB8::/32",
		"127.0.0.0/8",
		"0.0.0.0/32",
		"169.254.0.0/16",
		"224.0.0.0/4",
		"::/128",
		"::1/128",
		"::/127",
		"fe80::/10",
		"ff00::/8",
		"::ffff:10.0.0.0/104",
		"not-a-cidr",
		" 10.0.0.0/8",
	}
	for _, cidr := range invalid {
		document := fmt.Sprintf(`{"version":1,"allowed_cidrs":[%q],"services":[]}`, cidr)
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "allowed_cidrs") {
			t.Errorf("CIDR %q error = %v", cidr, err)
		}
	}

	duplicate := `{"version":1,"allowed_cidrs":["10.0.0.0/8","10.0.0.0/8"],"services":[]}`
	if _, err := parseServiceHealthConfig([]byte(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate CIDR error = %v", err)
	}
}

func TestParseServiceHealthConfigRejectsInvalidHostsAndSuffixes(t *testing.T) {
	invalidHosts := []string{
		"Example.com",
		"example.com.",
		"*.example.com",
		"example.com:443",
		"example.com/path",
		" example.com",
		"example com",
		"example_com",
		"-example.com",
		"example-.com",
		"127.0.0.1",
	}
	for _, host := range invalidHosts {
		document := fmt.Sprintf(`{"version":1,"allowed_cidrs":["10.0.0.0/8"],"allowed_hosts":[%q],"services":[]}`, host)
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
			t.Errorf("host %q error = %v", host, err)
		}
	}

	invalidSuffixes := []string{
		"example.com",
		".Example.com",
		".example.com.",
		".*.example.com",
		".example.com:443",
		".",
	}
	for _, suffix := range invalidSuffixes {
		document := fmt.Sprintf(`{"version":1,"allowed_cidrs":["10.0.0.0/8"],"allowed_dns_suffixes":[%q],"services":[]}`, suffix)
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "allowed_dns_suffixes") {
			t.Errorf("suffix %q error = %v", suffix, err)
		}
	}

	for _, field := range []string{"allowed_hosts", "allowed_dns_suffixes"} {
		value := "backend"
		if field == "allowed_dns_suffixes" {
			value = ".example.com"
		}
		document := fmt.Sprintf(`{"version":1,"allowed_cidrs":["10.0.0.0/8"],%q:[%q,%q],"services":[]}`, field, value, value)
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("duplicate %s error = %v", field, err)
		}
	}
}

func TestParseServiceHealthConfigRejectsLimits(t *testing.T) {
	services := make([]string, maxServiceHealthServices+1)
	for index := range services {
		services[index] = fmt.Sprintf(`{"proxy_host_id":%d,"type":"tcp"}`, index+1)
	}
	document := `{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[` + strings.Join(services, ",") + `]}`
	if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("service limit error = %v", err)
	}

	cidrs := make([]string, maxServiceHealthAllowedCIDRs+1)
	for index := range cidrs {
		cidrs[index] = fmt.Sprintf("\"10.%d.0.0/16\"", index)
	}
	document = `{"version":1,"allowed_cidrs":[` + strings.Join(cidrs, ",") + `],"services":[]}`
	if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("CIDR limit error = %v", err)
	}

	for _, test := range []struct {
		field string
		limit int
		value func(int) string
	}{
		{field: "allowed_hosts", limit: maxServiceHealthAllowedHosts, value: func(index int) string { return fmt.Sprintf("host-%d", index) }},
		{field: "allowed_dns_suffixes", limit: maxServiceHealthAllowedDNSSuffixes, value: func(index int) string { return fmt.Sprintf(".suffix-%d.example", index) }},
	} {
		values := make([]string, test.limit+1)
		for index := range values {
			values[index] = fmt.Sprintf("%q", test.value(index))
		}
		document = fmt.Sprintf(`{"version":1,"allowed_cidrs":["10.0.0.0/8"],%q:[%s],"services":[]}`, test.field, strings.Join(values, ","))
		if _, err := parseServiceHealthConfig([]byte(document)); err == nil || !strings.Contains(err.Error(), "more than") {
			t.Errorf("%s limit error = %v", test.field, err)
		}
	}

	oversized := []byte(strings.Repeat(" ", maxServiceHealthConfigBytes+1))
	if _, err := parseServiceHealthConfig(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("document size error = %v", err)
	}
}

func TestLoadServiceHealthConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "health.json")
	if err := os.WriteFile(path, []byte(validServiceHealthDocument()), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadServiceHealthConfigFile(path)
	if err != nil {
		t.Fatalf("loadServiceHealthConfigFile() error = %v", err)
	}
	if !config.Enabled || len(config.Services) != 2 {
		t.Fatalf("config = %#v", config)
	}

	privateMissingPath := filepath.Join(dir, "private-service-name.json")
	_, err = loadServiceHealthConfigFile(privateMissingPath)
	if err == nil || strings.Contains(err.Error(), "private-service-name") {
		t.Fatalf("missing file error = %v", err)
	}

	_, err = loadServiceHealthConfigFile(dir)
	if err == nil || !strings.Contains(err.Error(), "regular file") || strings.Contains(err.Error(), dir) {
		t.Fatalf("directory error = %v", err)
	}

	oversizedPath := filepath.Join(dir, "oversized.json")
	if err := os.WriteFile(oversizedPath, []byte(strings.Repeat(" ", maxServiceHealthConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServiceHealthConfigFile(oversizedPath); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized file error = %v", err)
	}
}

func TestServiceHealthConfigLoaderForBlankPath(t *testing.T) {
	var loader serviceHealthConfigLoader = serviceHealthConfigLoaderForPath("  ")
	config, err := loader()
	if err != nil {
		t.Fatalf("blank loader error = %v", err)
	}
	if config == nil || config.Enabled || len(config.AllowedCIDRs) != 0 || len(config.Services) != 0 {
		t.Fatalf("blank loader config = %#v", config)
	}
}

func TestServiceHealthConfigErrorsDoNotExposePrivateValues(t *testing.T) {
	privateValues := []string{"private.internal", "private-health-path", "private-cidr"}
	documents := []string{
		`{"version":1,"allowed_cidrs":["private-cidr"],"services":[]}`,
		`{"version":1,"allowed_cidrs":["10.0.0.0/8"],"allowed_hosts":["private.internal:443"],"services":[]}`,
		`{"version":1,"allowed_cidrs":["10.0.0.0/8"],"services":[{"proxy_host_id":1,"type":"http","path":"private-health-path","accepted_statuses":[{"min":200,"max":399}]}]}`,
	}
	for _, document := range documents {
		_, err := parseServiceHealthConfig([]byte(document))
		if err == nil {
			t.Fatal("parseServiceHealthConfig() accepted private invalid value")
		}
		for _, privateValue := range privateValues {
			if strings.Contains(err.Error(), privateValue) {
				t.Fatalf("error exposed %q: %v", privateValue, err)
			}
		}
	}
}
