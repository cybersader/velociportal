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

// PORTAL_CUSTOM_ICONS=enabled opts into MCP-imported custom PNG logos for NPM
// cards. Absent means off. It adds no route, listener, mount or credential and
// requires every existing shared-editor MCP prerequisite plus a namespace.
func loadCustomIconsConfig(lookup configLookup, mode serviceEditMode, shared *ServiceMetadataEditorConfig, mcp *MCPConfig, namespace string) (bool, error) {
	value, present, err := lookup("PORTAL_CUSTOM_ICONS")
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	if value != "enabled" {
		return false, errors.New("PORTAL_CUSTOM_ICONS must be enabled when supplied")
	}
	if mode != serviceEditShared || shared == nil || mcp == nil || namespace == "" {
		return false, errors.New("PORTAL_CUSTOM_ICONS requires PORTAL_EDIT_MODE=shared, PORTAL_MCP_ORIGIN and PORTAL_STATE_NAMESPACE")
	}
	return true, nil
}
