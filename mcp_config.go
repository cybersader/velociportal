package main

import "errors"

// MCPConfig enables only the read-only compatibility prototype. It does not
// initialize an editor, personal store, session, or separate listener.
type MCPConfig struct {
	Origin string
	Host   string
}

func loadMCPConfig(lookup configLookup, shared *ServiceMetadataEditorConfig, personal *PersonalServicesConfig) (*MCPConfig, error) {
	origin, present, err := lookup("PORTAL_MCP_ORIGIN")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	host, err := validatePortalPublicOrigin(origin)
	if err != nil {
		return nil, errors.New("PORTAL_MCP_ORIGIN must be a canonical HTTP(S) origin")
	}
	if shared != nil && shared.Origin != origin || personal != nil && personal.Origin != origin {
		return nil, errors.New("PORTAL_MCP_ORIGIN must match enabled shared and personal origins")
	}
	return &MCPConfig{Origin: origin, Host: host}, nil
}
