package main

import "errors"

// A nil config disables the feature entirely, independent of NPM edit mode.
type PersonalServicesConfig struct {
	Origin string
	Host   string
}

func loadPersonalServicesConfig(lookup configLookup, provider controlPlaneProvider) (*PersonalServicesConfig, error) {
	origin, present, err := lookup("PORTAL_PERSONAL_SERVICES_ORIGIN")
	if err != nil {
		return nil, err
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
	return &PersonalServicesConfig{Origin: origin, Host: host}, nil
}
