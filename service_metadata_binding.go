package main

import (
	"encoding/json"
	"errors"
	"strings"
)

// A value captured by each reader. It binds both selected catalogs without
// inferring source identity from endpoints, credentials, or stored mappings.
type serviceMetadataBinding struct {
	ControlPlane controlPlaneProvider
	Namespace    string
}

func (cfg *Config) metadataBinding() serviceMetadataBinding {
	return serviceMetadataBinding{ControlPlane: cfg.ControlPlane, Namespace: cfg.StateNamespace}
}

// Pure contextual validation, separate from syntax and the closed rollout gate.
// Legacy documents remain explicitly unbound. No mutation, adoption, or pruning.
func validateServiceMetadataBinding(document serviceMetadataDocument, binding serviceMetadataBinding) error {
	if !serviceMetadataBoundVersion(document.Version) {
		return nil
	}
	if !validStateNamespace(binding.Namespace) || document.Namespace != binding.Namespace ||
		(binding.ControlPlane != controlPlaneHeadscale && binding.ControlPlane != controlPlaneTailscale) ||
		document.ControlPlane != binding.ControlPlane ||
		(document.ControlPlane == controlPlaneHeadscale && (len(document.Machines) != 0 || len(document.Applications) != 0)) {
		return errors.New("service metadata binding is missing or does not match")
	}
	return nil
}

// All configured consumers use this parser. The existing immutable runtime gate
// runs first; once activated, exact binding is still mandatory before publication.
func parseBoundRuntimeServiceMetadata(data []byte, binding serviceMetadataBinding) (*ServiceMetadata, error) {
	metadata, err := parseRuntimeServiceMetadata(data)
	if err != nil {
		return nil, err
	}
	if err := validateServiceMetadataBinding(metadata.document, binding); err != nil {
		return nil, err
	}
	return metadata, nil
}

// Preflight v4 before starting readers/writers, including off/browser modes.
// Preserve legacy startup behavior: ordinary missing/unreadable/invalid metadata
// remains a complete-refresh failure handled by the poller, not a new startup
// requirement. This bounded header check grants nothing: v4 always goes through
// the full strict runtime parser and binding validator, never a partial view.
func preflightServiceMetadataBinding(path string, binding serviceMetadataBinding) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := readServiceMetadataFile(path)
	if err != nil {
		return nil
	}
	// Maps preserve key spelling: a case alias must not override the canonical
	// version and turn an invalid legacy refresh into a new startup failure.
	var header map[string]json.RawMessage
	var version int
	if json.Unmarshal(data, &header) != nil || json.Unmarshal(header["version"], &version) != nil || !serviceMetadataBoundVersion(version) {
		return nil
	}
	_, err = parseBoundRuntimeServiceMetadata(data, binding)
	return err
}
