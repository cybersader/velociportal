package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	serviceHealthConfigVersion = 1

	maxServiceHealthConfigBytes        = 256 * 1024
	maxServiceHealthServices           = 128
	maxServiceHealthAllowedCIDRs       = 128
	maxServiceHealthAllowedHosts       = 128
	maxServiceHealthAllowedDNSSuffixes = 128
	maxServiceHealthStatusRanges       = 8
	maxServiceHealthPathLength         = 2048
	maxServiceHealthHostLength         = 253

	defaultServiceHealthInterval = 60 * time.Second
	minServiceHealthInterval     = 15 * time.Second
	maxServiceHealthInterval     = 24 * time.Hour
	defaultServiceHealthTimeout  = 3 * time.Second
	minServiceHealthTimeout      = 250 * time.Millisecond
	maxServiceHealthTimeout      = 10 * time.Second
	defaultServiceHealthWorkers  = 4
	minServiceHealthWorkers      = 1
	maxServiceHealthWorkers      = 8
)

type ServiceHealthProbeType string

const (
	ServiceHealthProbeHTTP ServiceHealthProbeType = "http"
	ServiceHealthProbeTCP  ServiceHealthProbeType = "tcp"
)

type ServiceHealthConfig struct {
	Enabled            bool
	Interval           time.Duration
	Timeout            time.Duration
	Workers            int
	AllowedCIDRs       []netip.Prefix
	AllowedHosts       []string
	AllowedDNSSuffixes []string
	Services           []ServiceHealthService
}

type ServiceHealthService struct {
	ProxyHostID      int
	Type             ServiceHealthProbeType
	Path             string
	AcceptedStatuses []ServiceHealthStatusRange
}

type ServiceHealthStatusRange struct {
	Min int
	Max int
}

type serviceHealthConfigLoader func() (*ServiceHealthConfig, error)

func emptyServiceHealthConfig() *ServiceHealthConfig {
	return &ServiceHealthConfig{}
}

func serviceHealthConfigLoaderForPath(path string) serviceHealthConfigLoader {
	path = strings.TrimSpace(path)
	if path == "" {
		return func() (*ServiceHealthConfig, error) { return emptyServiceHealthConfig(), nil }
	}
	return func() (*ServiceHealthConfig, error) { return loadServiceHealthConfigFile(path) }
}

func loadServiceHealthConfigFile(path string) (*ServiceHealthConfig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, serviceHealthConfigFileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("service health configuration path is not a regular file")
	}
	if info.Size() > maxServiceHealthConfigBytes {
		return nil, fmt.Errorf("service health configuration file exceeds %d bytes", maxServiceHealthConfigBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, serviceHealthConfigFileError(err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxServiceHealthConfigBytes+1))
	if err != nil {
		return nil, errors.New("service health configuration file could not be read")
	}
	if len(data) > maxServiceHealthConfigBytes {
		return nil, fmt.Errorf("service health configuration file exceeds %d bytes", maxServiceHealthConfigBytes)
	}
	return parseServiceHealthConfig(data)
}

func serviceHealthConfigFileError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("service health configuration file does not exist")
	case errors.Is(err, os.ErrPermission):
		return errors.New("service health configuration file is not readable")
	default:
		return errors.New("service health configuration file could not be inspected")
	}
}

func parseServiceHealthConfig(data []byte) (*ServiceHealthConfig, error) {
	if len(data) > maxServiceHealthConfigBytes {
		return nil, fmt.Errorf("service health configuration exceeds %d bytes", maxServiceHealthConfigBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("service health configuration document is empty")
	}
	if err := rejectDuplicateServiceHealthJSONFields(data); err != nil {
		return nil, err
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return nil, errors.New("service health configuration document is invalid")
	}
	if !containsOnlyJSONFields(
		document,
		"version",
		"interval",
		"timeout",
		"workers",
		"allowed_cidrs",
		"allowed_hosts",
		"allowed_dns_suffixes",
		"services",
	) {
		return nil, errors.New("service health configuration document is invalid")
	}

	version, ok := decodeServiceHealthInt(document, "version")
	if !ok || version != serviceHealthConfigVersion {
		return nil, fmt.Errorf("service health configuration version must be %d", serviceHealthConfigVersion)
	}

	interval, err := parseServiceHealthDuration(document, "interval", defaultServiceHealthInterval, minServiceHealthInterval, maxServiceHealthInterval)
	if err != nil {
		return nil, errors.New("service health configuration has an invalid interval")
	}
	timeout, err := parseServiceHealthDuration(document, "timeout", defaultServiceHealthTimeout, minServiceHealthTimeout, maxServiceHealthTimeout)
	if err != nil {
		return nil, errors.New("service health configuration has an invalid timeout")
	}

	workers := defaultServiceHealthWorkers
	if _, exists := document["workers"]; exists {
		var ok bool
		workers, ok = decodeServiceHealthInt(document, "workers")
		if !ok || workers < minServiceHealthWorkers || workers > maxServiceHealthWorkers {
			return nil, errors.New("service health configuration has invalid workers")
		}
	}

	allowedCIDRValues, ok := decodeServiceHealthStringArray(document, "allowed_cidrs")
	if !ok || len(allowedCIDRValues) == 0 {
		return nil, errors.New("service health configuration allowed_cidrs must be a nonempty array")
	}
	if len(allowedCIDRValues) > maxServiceHealthAllowedCIDRs {
		return nil, fmt.Errorf("service health configuration contains more than %d allowed_cidrs", maxServiceHealthAllowedCIDRs)
	}
	allowedCIDRs := make([]netip.Prefix, 0, len(allowedCIDRValues))
	seenCIDRs := make(map[netip.Prefix]bool, len(allowedCIDRValues))
	for index, value := range allowedCIDRValues {
		prefix, err := validateServiceHealthCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("service health configuration allowed_cidrs entry %d is invalid", index)
		}
		if seenCIDRs[prefix] {
			return nil, fmt.Errorf("service health configuration allowed_cidrs entry %d is a duplicate", index)
		}
		seenCIDRs[prefix] = true
		allowedCIDRs = append(allowedCIDRs, prefix)
	}

	allowedHosts, err := parseServiceHealthNameAllowlist(document, "allowed_hosts", maxServiceHealthAllowedHosts, false)
	if err != nil {
		return nil, err
	}
	allowedDNSSuffixes, err := parseServiceHealthNameAllowlist(document, "allowed_dns_suffixes", maxServiceHealthAllowedDNSSuffixes, true)
	if err != nil {
		return nil, err
	}

	serviceValues, ok := decodeServiceHealthRawArray(document, "services")
	if !ok {
		return nil, errors.New("service health configuration services must be an array")
	}
	if len(serviceValues) > maxServiceHealthServices {
		return nil, fmt.Errorf("service health configuration contains more than %d services", maxServiceHealthServices)
	}
	services := make([]ServiceHealthService, 0, len(serviceValues))
	seenProxyHostIDs := make(map[int]bool, len(serviceValues))
	for index, raw := range serviceValues {
		service, err := parseServiceHealthService(raw, index)
		if err != nil {
			return nil, err
		}
		if seenProxyHostIDs[service.ProxyHostID] {
			return nil, fmt.Errorf("service health configuration service %d duplicates a proxy_host_id", index)
		}
		seenProxyHostIDs[service.ProxyHostID] = true
		services = append(services, service)
	}

	return &ServiceHealthConfig{
		Enabled:            true,
		Interval:           interval,
		Timeout:            timeout,
		Workers:            workers,
		AllowedCIDRs:       allowedCIDRs,
		AllowedHosts:       allowedHosts,
		AllowedDNSSuffixes: allowedDNSSuffixes,
		Services:           services,
	}, nil
}

func parseServiceHealthService(raw json.RawMessage, index int) (ServiceHealthService, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ServiceHealthService{}, fmt.Errorf("service health configuration service %d is invalid", index)
	}
	if !containsOnlyJSONFields(fields, "proxy_host_id", "type", "path", "accepted_statuses") {
		return ServiceHealthService{}, fmt.Errorf("service health configuration service %d is invalid", index)
	}

	proxyHostID, ok := decodeServiceHealthInt(fields, "proxy_host_id")
	if !ok || proxyHostID <= 0 {
		return ServiceHealthService{}, fmt.Errorf("service health configuration service %d has an invalid proxy_host_id", index)
	}
	probeTypeValue, ok := decodeServiceHealthString(fields, "type")
	if !ok || (probeTypeValue != string(ServiceHealthProbeHTTP) && probeTypeValue != string(ServiceHealthProbeTCP)) {
		return ServiceHealthService{}, fmt.Errorf("service health configuration service %d has an invalid type", index)
	}

	service := ServiceHealthService{
		ProxyHostID: proxyHostID,
		Type:        ServiceHealthProbeType(probeTypeValue),
	}
	if service.Type == ServiceHealthProbeTCP {
		if _, exists := fields["path"]; exists {
			return ServiceHealthService{}, fmt.Errorf("service health configuration service %d tcp probe must not set path", index)
		}
		if _, exists := fields["accepted_statuses"]; exists {
			return ServiceHealthService{}, fmt.Errorf("service health configuration service %d tcp probe must not set accepted_statuses", index)
		}
		return service, nil
	}

	service.Path = "/"
	if _, exists := fields["path"]; exists {
		pathValue, ok := decodeServiceHealthString(fields, "path")
		if !ok || validateServiceHealthPath(pathValue) != nil {
			return ServiceHealthService{}, fmt.Errorf("service health configuration service %d has an invalid path", index)
		}
		service.Path = pathValue
	}

	statusValues, ok := decodeServiceHealthRawArray(fields, "accepted_statuses")
	if !ok || len(statusValues) == 0 {
		return ServiceHealthService{}, fmt.Errorf("service health configuration service %d accepted_statuses must be a nonempty array", index)
	}
	if len(statusValues) > maxServiceHealthStatusRanges {
		return ServiceHealthService{}, fmt.Errorf("service health configuration service %d contains more than %d accepted_statuses", index, maxServiceHealthStatusRanges)
	}
	service.AcceptedStatuses = make([]ServiceHealthStatusRange, 0, len(statusValues))
	for rangeIndex, rangeRaw := range statusValues {
		statusRange, err := parseServiceHealthStatusRange(rangeRaw)
		if err != nil {
			return ServiceHealthService{}, fmt.Errorf("service health configuration service %d has an invalid accepted_statuses range %d", index, rangeIndex)
		}
		if rangeIndex > 0 && statusRange.Min <= service.AcceptedStatuses[rangeIndex-1].Max {
			return ServiceHealthService{}, fmt.Errorf("service health configuration service %d accepted_statuses must be sorted and non-overlapping", index)
		}
		service.AcceptedStatuses = append(service.AcceptedStatuses, statusRange)
	}
	return service, nil
}

func parseServiceHealthStatusRange(raw json.RawMessage) (ServiceHealthStatusRange, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil || !containsOnlyJSONFields(fields, "min", "max") {
		return ServiceHealthStatusRange{}, errors.New("invalid status range")
	}
	minimum, minOK := decodeServiceHealthInt(fields, "min")
	maximum, maxOK := decodeServiceHealthInt(fields, "max")
	if !minOK || !maxOK || minimum < 100 || minimum > 599 || maximum < 100 || maximum > 599 || minimum > maximum {
		return ServiceHealthStatusRange{}, errors.New("invalid status range")
	}
	return ServiceHealthStatusRange{Min: minimum, Max: maximum}, nil
}

func parseServiceHealthDuration(document map[string]json.RawMessage, field string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	if _, exists := document[field]; !exists {
		return fallback, nil
	}
	value, ok := decodeServiceHealthString(document, field)
	if !ok || value == "" || strings.TrimSpace(value) != value || containsControl(value) {
		return 0, errors.New("invalid duration")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < minimum || duration > maximum {
		return 0, errors.New("invalid duration")
	}
	return duration, nil
}

func parseServiceHealthNameAllowlist(document map[string]json.RawMessage, field string, limit int, suffix bool) ([]string, error) {
	if _, exists := document[field]; !exists {
		return []string{}, nil
	}
	values, ok := decodeServiceHealthStringArray(document, field)
	if !ok {
		return nil, fmt.Errorf("service health configuration %s must be an array", field)
	}
	if len(values) > limit {
		return nil, fmt.Errorf("service health configuration contains more than %d %s", limit, field)
	}
	seen := make(map[string]bool, len(values))
	for index, value := range values {
		valid := validateServiceHealthHost(value) == nil
		if suffix {
			valid = validateServiceHealthDNSSuffix(value) == nil
		}
		if !valid {
			return nil, fmt.Errorf("service health configuration %s entry %d is invalid", field, index)
		}
		if seen[value] {
			return nil, fmt.Errorf("service health configuration %s entry %d is a duplicate", field, index)
		}
		seen[value] = true
	}
	return values, nil
}

func validateServiceHealthCIDR(value string) (netip.Prefix, error) {
	if value == "" || strings.TrimSpace(value) != value || containsControl(value) {
		return netip.Prefix{}, errors.New("cidr is invalid")
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix != prefix.Masked() || prefix.String() != value || prefix.Addr().Is4In6() {
		return netip.Prefix{}, errors.New("cidr is invalid")
	}
	if serviceHealthDangerousAddress(prefix.Addr()) && serviceHealthDangerousAddress(serviceHealthPrefixLastAddr(prefix)) {
		return netip.Prefix{}, errors.New("cidr is dangerous")
	}
	return prefix, nil
}

func serviceHealthDangerousAddress(address netip.Addr) bool {
	return address.IsUnspecified() ||
		address.IsLoopback() ||
		address.IsMulticast() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast()
}

func serviceHealthPrefixLastAddr(prefix netip.Prefix) netip.Addr {
	prefix = prefix.Masked()
	if prefix.Addr().Is4() {
		bytes4 := prefix.Addr().As4()
		for bit := prefix.Bits(); bit < 32; bit++ {
			bytes4[bit/8] |= byte(1 << (7 - bit%8))
		}
		return netip.AddrFrom4(bytes4)
	}
	bytes16 := prefix.Addr().As16()
	for bit := prefix.Bits(); bit < 128; bit++ {
		bytes16[bit/8] |= byte(1 << (7 - bit%8))
	}
	return netip.AddrFrom16(bytes16)
}

func validateServiceHealthPath(value string) error {
	if value == "" || len(value) > maxServiceHealthPathLength || strings.TrimSpace(value) != value || containsControl(value) {
		return errors.New("path is invalid")
	}
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#") {
		return errors.New("path is invalid")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.RequestURI() != value {
		return errors.New("path is invalid")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return errors.New("path is invalid")
		}
	}
	return nil
}

func validateServiceHealthHost(value string) error {
	if value == "" || len(value) > maxServiceHealthHostLength || strings.ToLower(value) != value || strings.TrimSpace(value) != value || strings.HasSuffix(value, ".") || containsControl(value) {
		return errors.New("host is invalid")
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return errors.New("host is invalid")
	}
	labels := strings.Split(value, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("host is invalid")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return errors.New("host is invalid")
			}
		}
	}
	return nil
}

func validateServiceHealthDNSSuffix(value string) error {
	if !strings.HasPrefix(value, ".") || len(value) < 2 {
		return errors.New("dns suffix is invalid")
	}
	return validateServiceHealthHost(value[1:])
}

func decodeServiceHealthInt(fields map[string]json.RawMessage, field string) (int, bool) {
	raw, exists := fields[field]
	if !exists {
		return 0, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func decodeServiceHealthString(fields map[string]json.RawMessage, field string) (string, bool) {
	raw, exists := fields[field]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !utf8.ValidString(value) {
		return "", false
	}
	return value, true
}

func decodeServiceHealthStringArray(fields map[string]json.RawMessage, field string) ([]string, bool) {
	raw, exists := fields[field]
	if !exists {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func decodeServiceHealthRawArray(fields map[string]json.RawMessage, field string) ([]json.RawMessage, bool) {
	raw, exists := fields[field]
	if !exists {
		return nil, false
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func rejectDuplicateServiceHealthJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkServiceHealthJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("service health configuration document contains trailing data")
	}
	return nil
}

func walkServiceHealthJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("service health configuration document is invalid")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("service health configuration document is invalid")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("service health configuration document is invalid")
			}
			canonicalKey := strings.ToLower(key)
			if seen[canonicalKey] {
				return errors.New("service health configuration document contains a duplicate field")
			}
			seen[canonicalKey] = true
			if err := walkServiceHealthJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("service health configuration document is invalid")
		}
	case '[':
		for decoder.More() {
			if err := walkServiceHealthJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("service health configuration document is invalid")
		}
	default:
		return errors.New("service health configuration document is invalid")
	}
	return nil
}
