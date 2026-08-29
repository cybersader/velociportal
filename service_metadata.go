package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	serviceMetadataVersion    = 1
	maxServiceMetadataBytes   = 256 * 1024
	maxServiceMetadataEntries = 1024
	maxServiceMetadataName    = 120
	maxServiceMetadataURL     = 2048
)

type ServiceMetadata struct {
	Overrides map[int]ServiceOverride
}

type ServiceOverride struct {
	Name string
	URL  string
}

type serviceMetadataDocument struct {
	Version  int                    `json:"version"`
	Services []serviceMetadataEntry `json:"services"`
}

type serviceMetadataEntry struct {
	ProxyHostID int     `json:"proxy_host_id"`
	Name        *string `json:"name,omitempty"`
	URL         *string `json:"url,omitempty"`
}

type serviceMetadataLoader func() (*ServiceMetadata, error)

func emptyServiceMetadata() *ServiceMetadata {
	return &ServiceMetadata{Overrides: map[int]ServiceOverride{}}
}

func serviceMetadataLoaderForPath(path string) serviceMetadataLoader {
	path = strings.TrimSpace(path)
	if path == "" {
		return func() (*ServiceMetadata, error) { return emptyServiceMetadata(), nil }
	}
	return func() (*ServiceMetadata, error) { return loadServiceMetadataFile(path) }
}

func loadServiceMetadataFile(path string) (*ServiceMetadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, serviceMetadataFileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("service metadata path is not a regular file")
	}
	if info.Size() > maxServiceMetadataBytes {
		return nil, fmt.Errorf("service metadata file exceeds %d bytes", maxServiceMetadataBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, serviceMetadataFileError(err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxServiceMetadataBytes+1))
	if err != nil {
		return nil, errors.New("service metadata file could not be read")
	}
	if len(data) > maxServiceMetadataBytes {
		return nil, fmt.Errorf("service metadata file exceeds %d bytes", maxServiceMetadataBytes)
	}
	return parseServiceMetadata(data)
}

func serviceMetadataFileError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("service metadata file does not exist")
	case errors.Is(err, os.ErrPermission):
		return errors.New("service metadata file is not readable")
	default:
		return errors.New("service metadata file could not be inspected")
	}
}

func parseServiceMetadata(data []byte) (*ServiceMetadata, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("service metadata document is empty")
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return nil, err
	}
	if err := rejectNonCanonicalServiceMetadataFields(data); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document serviceMetadataDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("service metadata document is invalid")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if document.Version != serviceMetadataVersion {
		return nil, fmt.Errorf("service metadata version must be %d", serviceMetadataVersion)
	}
	if document.Services == nil {
		return nil, errors.New("service metadata services must be an array")
	}
	if len(document.Services) > maxServiceMetadataEntries {
		return nil, fmt.Errorf("service metadata contains more than %d services", maxServiceMetadataEntries)
	}

	metadata := emptyServiceMetadata()
	for index, entry := range document.Services {
		if entry.ProxyHostID <= 0 {
			return nil, fmt.Errorf("service metadata entry %d has an invalid proxy_host_id", index)
		}
		if _, exists := metadata.Overrides[entry.ProxyHostID]; exists {
			return nil, fmt.Errorf("service metadata entry %d duplicates a proxy_host_id", index)
		}
		if entry.Name == nil && entry.URL == nil {
			return nil, fmt.Errorf("service metadata entry %d must set name or url", index)
		}

		override := ServiceOverride{}
		if entry.Name != nil {
			name, err := validateServiceMetadataName(*entry.Name)
			if err != nil {
				return nil, fmt.Errorf("service metadata entry %d has an invalid name", index)
			}
			override.Name = name
		}
		if entry.URL != nil {
			normalized, err := validateServiceMetadataURL(*entry.URL)
			if err != nil {
				return nil, fmt.Errorf("service metadata entry %d has an invalid url", index)
			}
			override.URL = normalized
		}
		metadata.Overrides[entry.ProxyHostID] = override
	}
	return metadata, nil
}

func validateServiceMetadataName(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("name must be canonical")
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxServiceMetadataName || containsControl(value) {
		return "", errors.New("name is invalid")
	}
	return value, nil
}

func validateServiceMetadataURL(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxServiceMetadataURL || containsControl(value) {
		return "", errors.New("url is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return "", errors.New("url is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("url is invalid")
	}
	if parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" || strings.Contains(parsed.Hostname(), "*") {
		return "", errors.New("url is invalid")
	}
	return parsed.String(), nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	}
	return errors.New("service metadata document contains trailing data")
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func rejectNonCanonicalServiceMetadataFields(data []byte) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return errors.New("service metadata document is invalid")
	}
	if document == nil {
		return nil
	}
	if !containsOnlyJSONFields(
		document,
		"version",
		"services",
	) {
		return errors.New("service metadata document is invalid")
	}

	servicesData, exists := document["services"]
	if !exists {
		return nil
	}
	var services []json.RawMessage
	if err := json.Unmarshal(servicesData, &services); err != nil {
		return nil
	}
	for _, serviceData := range services {
		var service map[string]json.RawMessage
		if err := json.Unmarshal(serviceData, &service); err != nil ||
			service == nil {
			continue
		}
		if !containsOnlyJSONFields(
			service,
			"proxy_host_id",
			"name",
			"url",
		) {
			return errors.New("service metadata document is invalid")
		}
	}
	return nil
}

func containsOnlyJSONFields(
	fields map[string]json.RawMessage,
	allowed ...string,
) bool {
	allowedFields := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = true
	}
	for field := range fields {
		if !allowedFields[field] {
			return false
		}
	}
	return true
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("service metadata document is invalid")
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
				return errors.New("service metadata document is invalid")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("service metadata document is invalid")
			}
			canonicalKey := strings.ToLower(key)
			if seen[canonicalKey] {
				return errors.New("service metadata document contains a duplicate field")
			}
			seen[canonicalKey] = true
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("service metadata document is invalid")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("service metadata document is invalid")
		}
	default:
		return errors.New("service metadata document is invalid")
	}
	return nil
}

func unmatchedServiceMetadataCount(metadata *ServiceMetadata, proxyHosts []ProxyHost) int {
	if metadata == nil || len(metadata.Overrides) == 0 {
		return 0
	}
	known := make(map[int]bool, len(proxyHosts))
	for _, proxyHost := range proxyHosts {
		known[proxyHost.ID] = true
	}
	unmatched := 0
	for id := range metadata.Overrides {
		if !known[id] {
			unmatched++
		}
	}
	return unmatched
}
