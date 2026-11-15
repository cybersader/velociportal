package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

// One immutable source rollout boundary, enabled with bound readers, typed writer
// transitions and concrete application dispatch. This is not a deployment switch.
const serviceMetadataV4RuntimeEnabled = true

type deviceIDKind string

const (
	deviceIDKindID     deviceIDKind = "id"
	deviceIDKindNodeID deviceIDKind = "nodeId"
)

// ProviderDeviceID describes stored syntax only. No provider evidence is inferred
// from a stored value, Node.ID, hostname or address.
type ProviderDeviceID struct {
	Provider controlPlaneProvider
	Kind     deviceIDKind
	Value    string
}
type machineIconEntry struct {
	DeviceIDType deviceIDKind `json:"device_id_type"`
	DeviceID     string       `json:"device_id"`
	Icon         string       `json:"icon"`
}
type applicationIconEntry struct {
	TargetDigest string `json:"target_digest"`
	Icon         string `json:"icon"`
}

func parseRuntimeServiceMetadata(data []byte) (*ServiceMetadata, error) {
	metadata, err := parseServiceMetadata(data)
	if err != nil {
		return nil, err
	}
	if !serviceMetadataV4RuntimeEnabled && metadata.document.Version == serviceMetadataVersionV4 {
		return nil, errors.New("service metadata version is not enabled")
	}
	return metadata, nil
}

func serviceMetadataJSONDepth(data []byte) bool {
	depth, quoted, escaped := 0, false, false
	for _, c := range data {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > 16 {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}

func validMetadataNamespace(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for i, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-')) {
			return false
		}
	}
	return true
}
func validPresentationDeviceID(kind deviceIDKind, value string) bool {
	if kind != deviceIDKindID && kind != deviceIDKindNodeID || len(value) < 1 || len(value) > 128 {
		return false
	}
	for i, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-')) {
			return false
		}
	}
	return true
}
func validApplicationTargetDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Raw-array bounds precede typed decoding and index allocation. The duplicate
// walker already ran after the depth bound; scalar validation prevents Go's
// replacement of malformed Unicode from silently changing persisted values.
func validateServiceMetadataV4Fields(data []byte, fields map[string]json.RawMessage) error {
	invalid := errors.New("service metadata version 4 document is invalid")
	if len(fields) != 6 || !containsOnlyJSONFields(fields, "version", "namespace", "control_plane", "services", "machines", "applications") || !utf8.Valid(data) || !json.Valid(data) || !personalJSONScalars(data) {
		return invalid
	}
	if !bytes.Equal(bytes.TrimSpace(fields["version"]), []byte("4")) {
		return invalid
	}
	for _, name := range []string{"namespace", "control_plane"} {
		raw := bytes.TrimSpace(fields[name])
		var value string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
			return invalid
		}
	}
	total := 0
	for _, name := range []string{"services", "machines", "applications"} {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(fields[name], &rows) != nil || rows == nil {
			return invalid
		}
		total += len(rows)
		if total > maxServiceMetadataEntries {
			return errors.New("service metadata contains more than 1024 entries")
		}
		for _, row := range rows {
			if row == nil {
				return invalid
			}
			var keys []string
			switch name {
			case "machines":
				keys = []string{"device_id_type", "device_id", "icon"}
			case "applications":
				keys = []string{"target_digest", "icon"}
			default:
				continue // incumbent NPM field validators run next
			}
			if len(row) != len(keys) || !containsOnlyJSONFields(row, keys...) {
				return invalid
			}
			for _, key := range keys {
				raw := bytes.TrimSpace(row[key])
				var value string
				if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
					return invalid
				}
			}
		}
	}
	return nil
}

func indexServiceMetadataV4(metadata *ServiceMetadata) error {
	d := metadata.document
	invalid := errors.New("service metadata version 4 document is invalid")
	if !validMetadataNamespace(d.Namespace) || (d.ControlPlane != controlPlaneHeadscale && d.ControlPlane != controlPlaneTailscale) || d.Machines == nil || d.Applications == nil || len(d.Services)+len(d.Machines)+len(d.Applications) > maxServiceMetadataEntries {
		return invalid
	}
	if d.ControlPlane == controlPlaneHeadscale && (len(d.Machines) != 0 || len(d.Applications) != 0) {
		return invalid
	}
	metadata.MachineIcons = make(map[ProviderDeviceID]string, len(d.Machines))
	metadata.ApplicationIcons = make(map[string]string, len(d.Applications))
	for _, row := range d.Machines {
		key := ProviderDeviceID{controlPlaneTailscale, row.DeviceIDType, row.DeviceID}
		if !validPresentationDeviceID(row.DeviceIDType, row.DeviceID) || !validServiceIcon(row.Icon) {
			return invalid
		}
		if _, exists := metadata.MachineIcons[key]; exists {
			return invalid
		}
		metadata.MachineIcons[key] = row.Icon
	}
	for _, row := range d.Applications {
		if !validApplicationTargetDigest(row.TargetDigest) || !validServiceIcon(row.Icon) {
			return invalid
		}
		if _, exists := metadata.ApplicationIcons[row.TargetDigest]; exists {
			return invalid
		}
		metadata.ApplicationIcons[row.TargetDigest] = row.Icon
	}
	return nil
}

func serviceMetadataHasV4Fields(d serviceMetadataDocument) bool {
	return d.Namespace != "" || d.ControlPlane != "" || d.Machines != nil || d.Applications != nil
}

func cloneServiceMetadataDocument(d serviceMetadataDocument) serviceMetadataDocument {
	out := d
	if d.Services != nil {
		out.Services = append([]serviceMetadataEntry{}, d.Services...)
	}
	for i := range out.Services {
		row := &out.Services[i]
		for _, field := range []**string{&row.Name, &row.URL, &row.Category, &row.Icon} {
			if *field != nil {
				value := **field
				*field = &value
			}
		}
		if row.Order != nil {
			value := *row.Order
			row.Order = &value
		}
	}
	if d.Machines != nil {
		out.Machines = append([]machineIconEntry{}, d.Machines...)
	}
	if d.Applications != nil {
		out.Applications = append([]applicationIconEntry{}, d.Applications...)
	}
	return out
}

// Validate the complete in-memory model without imposing an encoded-output size.
// A compact valid input can expand beyond the output limit when escaped/indented;
// it must still be possible to shrink it. Only the resulting saved candidate is
// subject to canonical encoded size validation in the serializer below.
func validateServiceMetadataDocumentModel(d serviceMetadataDocument) error {
	invalid := errors.New("service metadata candidate is invalid")
	if d.Version < 1 || d.Version > 4 || d.Services == nil || len(d.Services)+len(d.Machines)+len(d.Applications) > maxServiceMetadataEntries {
		return invalid
	}
	if d.Version != serviceMetadataVersionV4 && serviceMetadataHasV4Fields(d) {
		return invalid
	}
	if d.Version == serviceMetadataVersionV4 {
		if err := indexServiceMetadataV4(&ServiceMetadata{document: d}); err != nil {
			return err
		}
	}
	ids := make(map[int]bool, len(d.Services))
	for _, row := range d.Services {
		if row.ProxyHostID <= 0 || ids[row.ProxyHostID] || row.NameNull && row.Name != nil || row.URLNull && row.URL != nil {
			return invalid
		}
		ids[row.ProxyHostID] = true
		if d.Version == serviceMetadataVersionV1 && (row.Category != nil || row.Order != nil) || d.Version < serviceMetadataVersionV3 && row.Icon != nil {
			return invalid
		}
		if row.Name == nil && row.URL == nil && row.Category == nil && row.Order == nil && row.Icon == nil && !(d.Version == serviceMetadataVersionV4 && (row.NameNull || row.URLNull)) {
			return invalid
		}
		if row.Name != nil {
			if _, err := validateServiceMetadataName(*row.Name); err != nil {
				return invalid
			}
		}
		if row.URL != nil {
			if !utf8.ValidString(*row.URL) {
				return invalid
			}
			if _, err := validateServiceMetadataURL(*row.URL); err != nil {
				return invalid
			}
		}
		if row.Category != nil {
			if _, err := validateServiceMetadataCategory(*row.Category); err != nil {
				return invalid
			}
		}
		if row.Order != nil && (*row.Order < 0 || *row.Order > maxServiceMetadataOrder) || row.Icon != nil && !validServiceIcon(*row.Icon) {
			return invalid
		}
	}
	return nil
}

// The operation selects the version; this encoder never upgrades or downcasts.
// Input byte bounds stay in parseServiceMetadata; output bounds apply only here.
func serializeServiceMetadataDocument(d serviceMetadataDocument) ([]byte, error) {
	if err := validateServiceMetadataDocumentModel(d); err != nil {
		return nil, err
	}
	invalid := errors.New("service metadata candidate is invalid")
	d = cloneServiceMetadataDocument(d)
	sort.Slice(d.Services, func(i, j int) bool { return d.Services[i].ProxyHostID < d.Services[j].ProxyHostID })
	sort.Slice(d.Machines, func(i, j int) bool {
		a, b := d.Machines[i], d.Machines[j]
		if a.DeviceIDType != b.DeviceIDType {
			return a.DeviceIDType < b.DeviceIDType
		}
		return a.DeviceID < b.DeviceID
	})
	sort.Slice(d.Applications, func(i, j int) bool { return d.Applications[i].TargetDigest < d.Applications[j].TargetDigest })
	type wireEntry struct {
		ProxyHostID int             `json:"proxy_host_id"`
		Name        json.RawMessage `json:"name,omitempty"`
		URL         json.RawMessage `json:"url,omitempty"`
		Category    *string         `json:"category,omitempty"`
		Order       *int            `json:"order,omitempty"`
		Icon        *string         `json:"icon,omitempty"`
	}
	optional := func(value *string, null bool) json.RawMessage {
		if null {
			return json.RawMessage("null")
		}
		if value == nil {
			return nil
		}
		raw, _ := json.Marshal(*value)
		return raw
	}
	services := make([]wireEntry, 0, len(d.Services))
	for _, row := range d.Services {
		services = append(services, wireEntry{row.ProxyHostID, optional(row.Name, row.NameNull), optional(row.URL, row.URLNull), row.Category, row.Order, row.Icon})
	}
	var wire any
	if d.Version == serviceMetadataVersionV4 {
		wire = struct {
			Version      int                    `json:"version"`
			Namespace    string                 `json:"namespace"`
			ControlPlane controlPlaneProvider   `json:"control_plane"`
			Services     []wireEntry            `json:"services"`
			Machines     []machineIconEntry     `json:"machines"`
			Applications []applicationIconEntry `json:"applications"`
		}{d.Version, d.Namespace, d.ControlPlane, services, d.Machines, d.Applications}
	} else {
		wire = struct {
			Version  int         `json:"version"`
			Services []wireEntry `json:"services"`
		}{d.Version, services}
	}
	data, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return nil, invalid
	}
	data = append(data, '\n')
	if _, err := parseServiceMetadata(data); err != nil {
		return nil, err
	}
	return data, nil
}

// Pure document operations only: no handler, authority, runtime upgrade or I/O.
// An icon reset differs deliberately from the legacy full-dialog reset.
func mutateServiceMetadataIcon(d serviceMetadataDocument, id int, icon string) (serviceMetadataDocument, error) {
	if d.Version != serviceMetadataVersionV4 || id <= 0 || icon != "" && !validServiceIcon(icon) {
		return serviceMetadataDocument{}, errors.New("invalid icon target")
	}
	if err := validateServiceMetadataDocumentModel(d); err != nil {
		return serviceMetadataDocument{}, err
	}
	out := cloneServiceMetadataDocument(d)
	found := false
	for i := range out.Services {
		row := &out.Services[i]
		if row.ProxyHostID != id {
			continue
		}
		found = true
		row.Icon = nil
		if icon != "" {
			row.Icon = &icon
		}
		if row.Name == nil && row.URL == nil && row.Category == nil && row.Order == nil && row.Icon == nil && !row.NameNull && !row.URLNull {
			out.Services = append(out.Services[:i], out.Services[i+1:]...)
		}
		break
	}
	if !found && icon != "" {
		out.Services = append(out.Services, serviceMetadataEntry{ProxyHostID: id, Icon: &icon})
	}
	if _, err := serializeServiceMetadataDocument(out); err != nil {
		return serviceMetadataDocument{}, err
	}
	return out, nil
}
func mutateMachineMetadataIcon(d serviceMetadataDocument, key ProviderDeviceID, icon string) (serviceMetadataDocument, error) {
	if d.Version != serviceMetadataVersionV4 || d.ControlPlane != controlPlaneTailscale || key.Provider != controlPlaneTailscale || !validPresentationDeviceID(key.Kind, key.Value) || icon != "" && !validServiceIcon(icon) {
		return serviceMetadataDocument{}, errors.New("invalid icon target")
	}
	if err := validateServiceMetadataDocumentModel(d); err != nil {
		return serviceMetadataDocument{}, err
	}
	out := cloneServiceMetadataDocument(d)
	found := false
	for i, row := range out.Machines {
		if row.DeviceIDType != key.Kind || row.DeviceID != key.Value {
			continue
		}
		found = true
		if icon == "" {
			out.Machines = append(out.Machines[:i], out.Machines[i+1:]...)
		} else {
			out.Machines[i].Icon = icon
		}
		break
	}
	if !found && icon != "" {
		out.Machines = append(out.Machines, machineIconEntry{key.Kind, key.Value, icon})
	}
	if _, err := serializeServiceMetadataDocument(out); err != nil {
		return serviceMetadataDocument{}, err
	}
	return out, nil
}
func mutateApplicationMetadataIcon(d serviceMetadataDocument, digest, icon string) (serviceMetadataDocument, error) {
	if d.Version != serviceMetadataVersionV4 || d.ControlPlane != controlPlaneTailscale || !validApplicationTargetDigest(digest) || icon != "" && !validServiceIcon(icon) {
		return serviceMetadataDocument{}, errors.New("invalid icon target")
	}
	if err := validateServiceMetadataDocumentModel(d); err != nil {
		return serviceMetadataDocument{}, err
	}
	out := cloneServiceMetadataDocument(d)
	found := false
	for i, row := range out.Applications {
		if row.TargetDigest != digest {
			continue
		}
		found = true
		if icon == "" {
			out.Applications = append(out.Applications[:i], out.Applications[i+1:]...)
		} else {
			out.Applications[i].Icon = icon
		}
		break
	}
	if !found && icon != "" {
		out.Applications = append(out.Applications, applicationIconEntry{digest, icon})
	}
	if _, err := serializeServiceMetadataDocument(out); err != nil {
		return serviceMetadataDocument{}, err
	}
	return out, nil
}
