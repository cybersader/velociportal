package main

import (
	"errors"
	"path/filepath"
	"strings"
)

// A nil config disables the feature entirely, independent of NPM edit mode.
type PersonalServicesConfig struct {
	Origin    string
	Host      string
	File      string
	Namespace string
	// Runtime supplies every configured presentation/health path before opening
	// either writer. The private backend also excludes each path's lock file.
	ForbiddenPaths []string
}

func validPersonalFile(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && strings.TrimSpace(path) == path && !containsControl(path)
}

func loadPersonalServicesConfig(lookup configLookup, provider controlPlaneProvider, namespace string) (*PersonalServicesConfig, error) {
	origin, present, err := lookup("PORTAL_PERSONAL_SERVICES_ORIGIN")
	if err != nil {
		return nil, err
	}
	file, filePresent, err := lookup("PORTAL_PERSONAL_SERVICES_FILE")
	if err != nil {
		return nil, err
	}
	if filePresent && (!present || !validPersonalFile(file)) {
		return nil, errors.New("PORTAL_PERSONAL_SERVICES_FILE requires enabled personal services and a canonical absolute file path")
	}
	if filePresent && !validStateNamespace(namespace) {
		return nil, errors.New("PORTAL_PERSONAL_SERVICES_FILE requires a valid PORTAL_STATE_NAMESPACE")
	}
	if !filePresent {
		// The global namespace may belong to shared metadata, not this RAM store.
		namespace = ""
	}
	if !present {
		return nil, nil
	}
	if provider != controlPlaneTailscale {
		return nil, errors.New("PORTAL_PERSONAL_SERVICES_ORIGIN requires CONTROL_PLANE=tailscale")
	}
	host, err := validatePortalPublicOrigin(origin)
	if err != nil {
		return nil, errors.New("PORTAL_PERSONAL_SERVICES_ORIGIN must be a canonical HTTP(S) origin")
	}
	return &PersonalServicesConfig{Origin: origin, Host: host, File: file, Namespace: namespace}, nil
}
