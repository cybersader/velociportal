package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ServiceMetadataEditorConfig contains only explicit operator configuration.
// The empty configuration is read-only; there is no inferred role membership.
type ServiceMetadataEditorConfig struct {
	Logins map[string]bool
	Origin string
	Host   string
}

func loadServiceMetadataEditorConfig(lookup configLookup, path string) (*ServiceMetadataEditorConfig, error) {
	editors, editorsSet, err := lookup("PORTAL_EDITORS")
	if err != nil {
		return nil, err
	}
	origin, originSet, err := lookup("PORTAL_PUBLIC_ORIGIN")
	if err != nil {
		return nil, err
	}
	if !editorsSet && !originSet {
		return nil, nil
	}
	if !editorsSet || !originSet || path == "" {
		return nil, errors.New("PORTAL_EDITORS and PORTAL_PUBLIC_ORIGIN require each other and SERVICE_METADATA_FILE")
	}
	if len(editors) > 16*1024 || !utf8.ValidString(editors) {
		return nil, errors.New("PORTAL_EDITORS is invalid")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(editors))
	var logins []string
	if decoder.Decode(&logins) != nil || requireJSONEOF(decoder) != nil || logins == nil || len(logins) > 32 {
		return nil, errors.New("PORTAL_EDITORS must be a bounded JSON login array")
	}
	configured := make(map[string]bool, len(logins))
	for _, login := range logins {
		// Full login, not mail-address normalization: identity stays exactly as Serve
		// supplies it. No trim, role, alias or case-folded lookup ever follows.
		at := strings.IndexByte(login, '@')
		if len(login) > 254 || at <= 0 || at == len(login)-1 || strings.Count(login, "@") != 1 || strings.TrimSpace(login) != login || strings.ContainsAny(login, " \t\r\n") || containsControl(login) || configured[login] {
			return nil, errors.New("PORTAL_EDITORS contains an invalid or duplicate full login")
		}
		configured[login] = true
	}
	host, err := validatePortalPublicOrigin(origin)
	if err != nil {
		return nil, err
	}
	return &ServiceMetadataEditorConfig{Logins: configured, Origin: origin, Host: host}, nil
}

func validatePortalPublicOrigin(raw string) (string, error) {
	bad := errors.New("PORTAL_PUBLIC_ORIGIN must be a canonical HTTP(S) origin")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || raw != u.Scheme+"://"+u.Host || containsControl(raw) {
		return "", bad
	}
	host := u.Hostname()
	if host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "*%\\") || strings.HasSuffix(host, ".") {
		return "", bad
	}
	if ip := net.ParseIP(host); ip != nil {
		if host != ip.String() {
			return "", bad
		}
	} else {
		if len(host) > 253 {
			return "", bad
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", bad
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
					return "", bad
				}
			}
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", bad
	}
	canonicalHost := host
	if strings.Contains(host, ":") {
		canonicalHost = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port || (u.Scheme == "http" && number == 80) || (u.Scheme == "https" && number == 443) {
			return "", bad
		}
		canonicalHost = net.JoinHostPort(host, port)
	}
	if u.Host != canonicalHost {
		return "", bad
	}
	return u.Host, nil
}
